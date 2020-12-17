package ratelimit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	envoy_config_core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"github.com/envoyproxy/ratelimit/src/utils"
	"strings"
	"sync"
	"time"

	pb "github.com/envoyproxy/go-control-plane/envoy/service/ratelimit/v3"
	"github.com/envoyproxy/ratelimit/src/assert"
	"github.com/envoyproxy/ratelimit/src/config"
	"github.com/envoyproxy/ratelimit/src/limiter"
	"github.com/envoyproxy/ratelimit/src/redis"
	"github.com/lyft/goruntime/loader"
	stats "github.com/lyft/gostats"
	logger "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"golang.org/x/sync/semaphore"
	"os"
	"strconv"
)

type shouldRateLimitStats struct {
	redisError   stats.Counter
	serviceError stats.Counter
}

func newShouldRateLimitStats(scope stats.Scope) shouldRateLimitStats {
	ret := shouldRateLimitStats{}
	ret.redisError = scope.NewCounter("redis_error")
	ret.serviceError = scope.NewCounter("service_error")
	return ret
}

type serviceStats struct {
	configLoadSuccess stats.Counter
	configLoadError   stats.Counter
	shouldRateLimit   shouldRateLimitStats
}

func newServiceStats(scope stats.Scope) serviceStats {
	ret := serviceStats{}
	ret.configLoadSuccess = scope.NewCounter("config_load_success")
	ret.configLoadError = scope.NewCounter("config_load_error")
	ret.shouldRateLimit = newShouldRateLimitStats(scope.Scope("call.should_rate_limit"))
	return ret
}

type RateLimitServiceServer interface {
	pb.RateLimitServiceServer
	GetCurrentConfig() config.RateLimitConfig
	GetLegacyService() RateLimitLegacyServiceServer
}

type service struct {
	runtime            loader.IFace
	configLock         sync.RWMutex
	configLoader       config.RateLimitConfigLoader
	config             config.RateLimitConfig
	runtimeUpdateEvent chan int
	cache              limiter.RateLimitCache
	stats              serviceStats
	rlStatsScope       stats.Scope
	legacy             *legacyService
	runtimeWatchRoot   bool
	timeSource         limiter.TimeSource
	sleeperSemaphore   *semaphore.Weighted

	reportDetailSampler utils.Sampler
}

func (this *service) reloadConfig() {
	defer func() {
		if e := recover(); e != nil {
			configError, ok := e.(config.RateLimitConfigError)
			if !ok {
				panic(e)
			}

			this.stats.configLoadError.Inc()
			logger.Errorf("error loading new configuration from runtime: %s", configError.Error())
		}
	}()

	files := []config.RateLimitConfigToLoad{}
	snapshot := this.runtime.Snapshot()
	for _, key := range snapshot.Keys() {
		if this.runtimeWatchRoot && !strings.HasPrefix(key, "config.") {
			continue
		}

		files = append(files, config.RateLimitConfigToLoad{key, snapshot.Get(key)})
	}

	newConfig := this.configLoader.Load(files, this.rlStatsScope)
	this.stats.configLoadSuccess.Inc()
	this.configLock.Lock()
	this.config = newConfig
	this.configLock.Unlock()
}

type serviceError string

func (e serviceError) Error() string {
	return string(e)
}

func checkServiceErr(something bool, msg string) {
	if !something {
		panic(serviceError(msg))
	}
}

func (this *service) shouldRateLimitWorker(
	ctx context.Context, request *pb.RateLimitRequest) *pb.RateLimitResponse {

	checkServiceErr(request.Domain != "", "rate limit domain must not be empty")
	checkServiceErr(len(request.Descriptors) != 0, "rate limit descriptor list must not be empty")

	snappedConfig := this.GetCurrentConfig()
	checkServiceErr(snappedConfig != nil, "no rate limit configuration loaded")

	limitsToCheck := make([]*config.RateLimit, len(request.Descriptors))
	for i, descriptor := range request.Descriptors {
		if logger.IsLevelEnabled(logger.DebugLevel) {
			var descriptorEntryStrings []string
			for _, descriptorEntry := range descriptor.GetEntries() {
				descriptorEntryStrings = append(
					descriptorEntryStrings,
					fmt.Sprintf("(%s=%s)", descriptorEntry.Key, descriptorEntry.Value),
				)
			}
			logger.Debugf("got descriptor: %s", strings.Join(descriptorEntryStrings, ","))
		}
		limitsToCheck[i] = snappedConfig.GetLimit(ctx, request.Domain, descriptor)
		if logger.IsLevelEnabled(logger.DebugLevel) {
			if limitsToCheck[i] == nil {
				logger.Debugf("descriptor does not match any limit, no limits applied")
			} else {
				logger.Debugf(
					"applying limit: %d requests per %s",
					limitsToCheck[i].Limit.RequestsPerUnit,
					limitsToCheck[i].Limit.Unit.String(),
				)
			}
		}
	}

	doLimitResponse := this.cache.DoLimit(ctx, request, limitsToCheck)

	if doLimitResponse.SleepOnThrottle && doLimitResponse.ThrottleMillis > 0 {
		sem := this.sleeperSemaphore
		if sem != nil {
			if sem.TryAcquire(1) {
				defer sem.Release(1)
				logger.Warnf("near limit, sleeping %d", doLimitResponse.ThrottleMillis)
				this.timeSource.Sleep(time.Duration(doLimitResponse.ThrottleMillis) * time.Millisecond)
				doLimitResponse.ThrottleMillis = 0 // we throttle on the server side by sleeping, so reset this
			}
		}
	}

	responseDescriptorStatuses := doLimitResponse.DescriptorStatuses
	assert.Assert(len(limitsToCheck) == len(responseDescriptorStatuses))

	response := &pb.RateLimitResponse{}
	response.Statuses = make([]*pb.RateLimitResponse_DescriptorStatus, len(request.Descriptors))
	finalCode := pb.RateLimitResponse_OK
	for i, descriptorStatus := range responseDescriptorStatuses {
		response.Statuses[i] = descriptorStatus
		if descriptorStatus.Code == pb.RateLimitResponse_OVER_LIMIT {
			finalCode = descriptorStatus.Code
		}
	}
	response.OverallCode = finalCode

	if doLimitResponse.ReportDetails && this.reportDetailSampler.Sample() {
		if status, err := json.Marshal(doLimitResponse); err == nil {
			encodedStatus := base64.RawURLEncoding.EncodeToString(status)
			header := &envoy_config_core_v3.HeaderValue{
				Key:   "x-ratelimit-details",
				Value: encodedStatus,
			}
			response.ResponseHeadersToAdd = append(response.ResponseHeadersToAdd, header)
		}
	}

	if doLimitResponse.ThrottleMillis > 0 {
		header := &envoy_config_core_v3.HeaderValue{
			Key:   "x-ratelimit-throttle-ms",
			Value: strconv.FormatInt(int64(doLimitResponse.ThrottleMillis), 10),
		}
		response.ResponseHeadersToAdd = append(response.ResponseHeadersToAdd, header)
	}

	return response
}

func (this *service) ShouldRateLimit(
	ctx context.Context,
	request *pb.RateLimitRequest) (finalResponse *pb.RateLimitResponse, finalError error) {

	defer func() {
		err := recover()
		if err == nil {
			return
		}

		logger.Debugf("caught error during call: %+v", err)
		finalResponse = nil
		switch t := err.(type) {
		case redis.RedisError:
			{
				this.stats.shouldRateLimit.redisError.Inc()
				finalError = t
			}
		case serviceError:
			{
				this.stats.shouldRateLimit.serviceError.Inc()
				finalError = t
			}
		default:
			panic(err)
		}
	}()

	logger.Debugf("domain: %s descriptors: %+v", request.GetDomain(), request.GetDescriptors())
	response := this.shouldRateLimitWorker(ctx, request)
	logger.Debugf("returning normal response: %+v", response)
	return response, nil
}

func (this *service) GetLegacyService() RateLimitLegacyServiceServer {
	return this.legacy
}

func (this *service) GetCurrentConfig() config.RateLimitConfig {
	this.configLock.RLock()
	defer this.configLock.RUnlock()
	return this.config
}

func NewService(runtime loader.IFace, cache limiter.RateLimitCache,
	configLoader config.RateLimitConfigLoader, stats stats.Scope, runtimeWatchRoot bool,
	timeSource limiter.TimeSource) RateLimitServiceServer {

	newService := &service{
		runtime:            runtime,
		configLock:         sync.RWMutex{},
		configLoader:       configLoader,
		config:             nil,
		runtimeUpdateEvent: make(chan int),
		cache:              cache,
		stats:              newServiceStats(stats),
		rlStatsScope:       stats.Scope("rate_limit"),
		runtimeWatchRoot:   runtimeWatchRoot,
		timeSource:         timeSource,
		sleeperSemaphore:   nil, // no sleeping by default
		reportDetailSampler: &utils.BurstSampler{
			Burst:       100,
			Period:      time.Second,
			NextSampler: utils.Sometimes,
		},
	}
	newService.legacy = &legacyService{
		s:                          newService,
		shouldRateLimitLegacyStats: newShouldRateLimitLegacyStats(stats),
	}

	runtime.AddUpdateCallback(newService.runtimeUpdateEvent)

	if s, ok := os.LookupEnv("MAX_SLEEPING_ROUTINES"); ok {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			newService.sleeperSemaphore = semaphore.NewWeighted(int64(v))
		}
	}

	newService.reloadConfig()

	go func() {
		// No exit right now.
		for {
			logger.Debugf("waiting for runtime update")
			<-newService.runtimeUpdateEvent
			logger.Debugf("got runtime update and reloading config")
			newService.reloadConfig()
		}
	}()

	return newService
}

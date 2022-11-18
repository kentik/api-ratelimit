package ratelimit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	envoy_config_core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"github.com/envoyproxy/ratelimit/src/utils"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	otl "github.com/opentracing/opentracing-go/log"
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
	timeSource         utils.TimeSource
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

	response := &pb.RateLimitResponse{}

	span := opentracing.SpanFromContext(ctx)
	if span != nil {
		span.LogFields(otl.String("event", "shouldRateLimitWorker.start"))
		defer span.LogFields(otl.String("event", "shouldRateLimitWorker.done"), otl.Int32("response.code", int32(response.OverallCode)))
	}

	checkServiceErr(request.Domain != "", "rate limit domain must not be empty")
	checkServiceErr(len(request.Descriptors) != 0, "rate limit descriptor list must not be empty")

	snappedConfig := this.GetCurrentConfig()
	checkServiceErr(snappedConfig != nil, "no rate limit configuration loaded")

	sleepOnThrottle := false
	reportDetails := false
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
		if limitsToCheck[i] != nil {
			if !sleepOnThrottle {
				sleepOnThrottle = limitsToCheck[i].SleepOnThrottle
			}
			if !reportDetails {
				reportDetails = limitsToCheck[i].ReportDetails
			}
		}
	}

	doLimitResponse := this.cache.DoLimit(ctx, request, limitsToCheck)
	assert.Assert(len(limitsToCheck) == len(doLimitResponse.DescriptorStatuses))

	if sleepOnThrottle && doLimitResponse.ThrottleMillis > 0 {
		var throttleSpan opentracing.Span
		if span != nil {
			throttleSpan = span.Tracer().StartSpan("sleep_on_throttle", opentracing.ChildOf(span.Context()))
			throttleSpan.SetTag("throttling.sleep_ms", doLimitResponse.ThrottleMillis)
		}

		sem := this.sleeperSemaphore
		if sem != nil {
			if sem.TryAcquire(1) {
				logger.Debugf("near limit, sleeping %d", doLimitResponse.ThrottleMillis)
				this.timeSource.Sleep(time.Duration(doLimitResponse.ThrottleMillis) * time.Millisecond)
				sem.Release(1)
				doLimitResponse.ThrottleMillis = 0 // we throttle on the server side by sleeping, so reset this
			} else {
				if throttleSpan != nil {
					throttleSpan.LogFields(otl.String("event", "throttling.sem_exhausted"))
					throttleSpan.SetTag("error", true)
				}
			}
		}

		if throttleSpan != nil {
			throttleSpan.Finish()
		}
	}

	response.Statuses = make([]*pb.RateLimitResponse_DescriptorStatus, len(request.Descriptors))
	finalCode := pb.RateLimitResponse_OK
	for i, descriptorStatus := range doLimitResponse.DescriptorStatuses {
		response.Statuses[i] = descriptorStatus
		if descriptorStatus.Code == pb.RateLimitResponse_OVER_LIMIT {
			finalCode = descriptorStatus.Code
		}
	}
	response.OverallCode = finalCode

	if span != nil {
		span.SetTag("OverallCode", finalCode)
	}

	if reportDetails {
		if this.reportDetailSampler.Sample() {
			if status, err := json.Marshal(doLimitResponse); err == nil {
				encodedStatus := base64.RawURLEncoding.EncodeToString(status)
				header := &envoy_config_core_v3.HeaderValue{
					Key:   "x-ratelimit-details",
					Value: encodedStatus,
				}
				response.ResponseHeadersToAdd = append(response.ResponseHeadersToAdd, header)
				if span != nil {
					span.LogFields(otl.String("event", "add_response_header"), otl.String("x-ratelimit-details", encodedStatus))
				}
			}
		}
		if doLimitResponse.ThrottleMillis > 0 {
			if span != nil {
				span.SetTag("ThrottleMillis", doLimitResponse.ThrottleMillis)
			}

			header := &envoy_config_core_v3.HeaderValue{
				Key:   "x-ratelimit-throttle-ms",
				Value: strconv.FormatInt(int64(doLimitResponse.ThrottleMillis), 10),
			}
			response.ResponseHeadersToAdd = append(response.ResponseHeadersToAdd, header)
			if span != nil {
				span.LogFields(otl.String("event", "add_response_header"), otl.Uint32("x-ratelimit-throttle-ms", doLimitResponse.ThrottleMillis))
			}
		}
	}

	return response
}

func (this *service) ShouldRateLimit(
	ctx context.Context,
	request *pb.RateLimitRequest) (finalResponse *pb.RateLimitResponse, finalError error) {

	span := opentracing.SpanFromContext(ctx)

	defer func() {
		err := recover()
		if err == nil {
			return
		}

		if span != nil {
			ext.Error.Set(span, true)
			span.LogFields(
				otl.Object("err", err),
				otl.String("msg", "could not unmarshal message, ignoring (potential data loss)"),
			)
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
	timeSource utils.TimeSource) RateLimitServiceServer {

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

package ratelimit_test

import (
	"github.com/envoyproxy/ratelimit/src/limiter"
	"sync"
	"testing"

	pb "github.com/envoyproxy/go-control-plane/envoy/service/ratelimit/v3"
	"github.com/envoyproxy/ratelimit/src/config"
	"github.com/envoyproxy/ratelimit/src/redis"
	ratelimit "github.com/envoyproxy/ratelimit/src/service"
	"github.com/envoyproxy/ratelimit/test/common"
	mock_config "github.com/envoyproxy/ratelimit/test/mocks/config"
	mock_limiter "github.com/envoyproxy/ratelimit/test/mocks/limiter"
	mock_loader "github.com/envoyproxy/ratelimit/test/mocks/runtime/loader"
	mock_snapshot "github.com/envoyproxy/ratelimit/test/mocks/runtime/snapshot"
	"github.com/golang/mock/gomock"
	stats "github.com/lyft/gostats"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
)

type barrier struct {
	ready bool
	event *sync.Cond
}

func (this *barrier) signal() {
	this.event.L.Lock()
	defer this.event.L.Unlock()
	this.ready = true
	this.event.Signal()
}

func (this *barrier) wait() {
	this.event.L.Lock()
	defer this.event.L.Unlock()
	if !this.ready {
		this.event.Wait()
	}
	this.ready = false
}

func newBarrier() barrier {
	ret := barrier{}
	ret.event = sync.NewCond(&sync.Mutex{})
	return ret
}

type rateLimitServiceTestSuite struct {
	assert                *assert.Assertions
	controller            *gomock.Controller
	runtime               *mock_loader.MockIFace
	snapshot              *mock_snapshot.MockIFace
	cache                 *mock_limiter.MockRateLimitCache
	configLoader          *mock_config.MockRateLimitConfigLoader
	config                *mock_config.MockRateLimitConfig
	runtimeUpdateCallback chan<- int
	statStore             stats.Store
}

func commonSetup(t *testing.T) rateLimitServiceTestSuite {
	ret := rateLimitServiceTestSuite{}
	ret.assert = assert.New(t)
	ret.controller = gomock.NewController(t)
	ret.runtime = mock_loader.NewMockIFace(ret.controller)
	ret.snapshot = mock_snapshot.NewMockIFace(ret.controller)
	ret.cache = mock_limiter.NewMockRateLimitCache(ret.controller)
	ret.configLoader = mock_config.NewMockRateLimitConfigLoader(ret.controller)
	ret.config = mock_config.NewMockRateLimitConfig(ret.controller)
	ret.statStore = stats.NewStore(stats.NewNullSink(), false)
	return ret
}

func (this *rateLimitServiceTestSuite) setupBasicService() ratelimit.RateLimitServiceServer {
	this.runtime.EXPECT().AddUpdateCallback(gomock.Any()).Do(
		func(callback chan<- int) {
			this.runtimeUpdateCallback = callback
		})
	this.runtime.EXPECT().Snapshot().Return(this.snapshot).MinTimes(1)
	this.snapshot.EXPECT().Keys().Return([]string{"foo", "config.basic_config"}).MinTimes(1)
	this.snapshot.EXPECT().Get("config.basic_config").Return("fake_yaml").MinTimes(1)
	this.configLoader.EXPECT().Load(
		[]config.RateLimitConfigToLoad{{"config.basic_config", "fake_yaml"}},
		gomock.Any()).Return(this.config)
	return ratelimit.NewService(this.runtime, this.cache, this.configLoader, this.statStore, true, nil)
}

func TestService(test *testing.T) {
	t := commonSetup(test)
	defer t.controller.Finish()
	service := t.setupBasicService()

	// First request, config should be loaded.
	request := common.NewRateLimitRequest("test-domain", [][][2]string{{{"hello", "world"}}}, 1)
	t.config.EXPECT().GetLimit(context.TODO(), "test-domain", request.Descriptors[0]).Return(nil)
	t.cache.EXPECT().DoLimit(context.TODO(), request, []*config.RateLimit{nil}).Return(
		&limiter.DoLimitResponse{
			DescriptorStatuses: []*pb.RateLimitResponse_DescriptorStatus{{Code: pb.RateLimitResponse_OK, CurrentLimit: nil, LimitRemaining: 0}},
		})

	response, err := service.ShouldRateLimit(context.TODO(), request)
	common.AssertProtoEqual(
		t.assert,
		&pb.RateLimitResponse{
			OverallCode: pb.RateLimitResponse_OK,
			Statuses:    []*pb.RateLimitResponse_DescriptorStatus{{Code: pb.RateLimitResponse_OK, CurrentLimit: nil, LimitRemaining: 0}}},
		response)
	t.assert.Nil(err)

	// Force a config reload.
	barrier := newBarrier()
	t.configLoader.EXPECT().Load(
		[]config.RateLimitConfigToLoad{{"config.basic_config", "fake_yaml"}}, gomock.Any()).Do(
		func([]config.RateLimitConfigToLoad, stats.Scope) { barrier.signal() }).Return(t.config)
	t.runtimeUpdateCallback <- 1
	barrier.wait()

	// Different request.
	request = common.NewRateLimitRequest(
		"different-domain", [][][2]string{{{"foo", "bar"}}, {{"hello", "world"}}}, 1)
	limits := []*config.RateLimit{
		config.NewRateLimit(10, pb.RateLimitResponse_RateLimit_MINUTE, "key", t.statStore, false, false),
		nil}
	t.config.EXPECT().GetLimit(context.TODO(), "different-domain", request.Descriptors[0]).Return(limits[0])
	t.config.EXPECT().GetLimit(context.TODO(), "different-domain", request.Descriptors[1]).Return(limits[1])
	t.cache.EXPECT().DoLimit(context.TODO(), request, limits).Return(
		&limiter.DoLimitResponse{
			DescriptorStatuses: []*pb.RateLimitResponse_DescriptorStatus{{Code: pb.RateLimitResponse_OVER_LIMIT, CurrentLimit: limits[0].Limit, LimitRemaining: 0},
				{Code: pb.RateLimitResponse_OK, CurrentLimit: nil, LimitRemaining: 0}}})
	response, err = service.ShouldRateLimit(context.TODO(), request)
	common.AssertProtoEqual(
		t.assert,
		&pb.RateLimitResponse{
			OverallCode: pb.RateLimitResponse_OVER_LIMIT,
			Statuses: []*pb.RateLimitResponse_DescriptorStatus{
				{Code: pb.RateLimitResponse_OVER_LIMIT, CurrentLimit: limits[0].Limit, LimitRemaining: 0},
				{Code: pb.RateLimitResponse_OK, CurrentLimit: nil, LimitRemaining: 0},
			}},
		response)
	t.assert.Nil(err)

	// Config load failure.
	t.configLoader.EXPECT().Load(
		[]config.RateLimitConfigToLoad{{"config.basic_config", "fake_yaml"}}, gomock.Any()).Do(
		func([]config.RateLimitConfigToLoad, stats.Scope) {
			barrier.signal()
			panic(config.RateLimitConfigError("load error"))
		})
	t.runtimeUpdateCallback <- 1
	barrier.wait()

	// Config should still be valid. Also make sure order does not affect results.
	limits = []*config.RateLimit{
		nil,
		config.NewRateLimit(10, pb.RateLimitResponse_RateLimit_MINUTE, "key", t.statStore, false, false)}
	t.config.EXPECT().GetLimit(context.TODO(), "different-domain", request.Descriptors[0]).Return(limits[0])
	t.config.EXPECT().GetLimit(context.TODO(), "different-domain", request.Descriptors[1]).Return(limits[1])
	t.cache.EXPECT().DoLimit(context.TODO(), request, limits).Return(
		&limiter.DoLimitResponse{
			DescriptorStatuses: []*pb.RateLimitResponse_DescriptorStatus{{Code: pb.RateLimitResponse_OK, CurrentLimit: nil, LimitRemaining: 0},
				{Code: pb.RateLimitResponse_OVER_LIMIT, CurrentLimit: limits[1].Limit, LimitRemaining: 0}},
		})
	response, err = service.ShouldRateLimit(context.TODO(), request)
	common.AssertProtoEqual(
		t.assert,
		&pb.RateLimitResponse{
			OverallCode: pb.RateLimitResponse_OVER_LIMIT,
			Statuses: []*pb.RateLimitResponse_DescriptorStatus{
				{Code: pb.RateLimitResponse_OK, CurrentLimit: nil, LimitRemaining: 0},
				{Code: pb.RateLimitResponse_OVER_LIMIT, CurrentLimit: limits[1].Limit, LimitRemaining: 0},
			}},
		response)
	t.assert.Nil(err)

	t.assert.EqualValues(2, t.statStore.NewCounter("config_load_success").Value())
	t.assert.EqualValues(1, t.statStore.NewCounter("config_load_error").Value())
}

func TestEmptyDomain(test *testing.T) {
	t := commonSetup(test)
	defer t.controller.Finish()
	service := t.setupBasicService()

	request := common.NewRateLimitRequest("", [][][2]string{{{"hello", "world"}}}, 1)
	response, err := service.ShouldRateLimit(context.TODO(), request)
	t.assert.Nil(response)
	t.assert.Equal("rate limit domain must not be empty", err.Error())
	t.assert.EqualValues(1, t.statStore.NewCounter("call.should_rate_limit.service_error").Value())
}

func TestEmptyDescriptors(test *testing.T) {
	t := commonSetup(test)
	defer t.controller.Finish()
	service := t.setupBasicService()

	request := common.NewRateLimitRequest("test-domain", [][][2]string{}, 1)
	response, err := service.ShouldRateLimit(context.TODO(), request)
	t.assert.Nil(response)
	t.assert.Equal("rate limit descriptor list must not be empty", err.Error())
	t.assert.EqualValues(1, t.statStore.NewCounter("call.should_rate_limit.service_error").Value())
}

func TestCacheError(test *testing.T) {
	t := commonSetup(test)
	defer t.controller.Finish()
	service := t.setupBasicService()

	request := common.NewRateLimitRequest("different-domain", [][][2]string{{{"foo", "bar"}}}, 1)
	limits := []*config.RateLimit{config.NewRateLimit(10, pb.RateLimitResponse_RateLimit_MINUTE, "key", t.statStore, false, false)}
	t.config.EXPECT().GetLimit(context.TODO(), "different-domain", request.Descriptors[0]).Return(limits[0])
	t.cache.EXPECT().DoLimit(context.TODO(), request, limits).Do(
		func(context.Context, *pb.RateLimitRequest, []*config.RateLimit) {
			panic(redis.RedisError("cache error"))
		})

	response, err := service.ShouldRateLimit(context.TODO(), request)
	t.assert.Nil(response)
	t.assert.Equal("cache error", err.Error())
	t.assert.EqualValues(1, t.statStore.NewCounter("call.should_rate_limit.redis_error").Value())
}

func TestInitialLoadError(test *testing.T) {
	t := commonSetup(test)
	defer t.controller.Finish()

	t.runtime.EXPECT().AddUpdateCallback(gomock.Any()).Do(
		func(callback chan<- int) { t.runtimeUpdateCallback = callback })
	t.runtime.EXPECT().Snapshot().Return(t.snapshot).MinTimes(1)
	t.snapshot.EXPECT().Keys().Return([]string{"foo", "config.basic_config"}).MinTimes(1)
	t.snapshot.EXPECT().Get("config.basic_config").Return("fake_yaml").MinTimes(1)
	t.configLoader.EXPECT().Load(
		[]config.RateLimitConfigToLoad{{"config.basic_config", "fake_yaml"}}, gomock.Any()).Do(
		func([]config.RateLimitConfigToLoad, stats.Scope) {
			panic(config.RateLimitConfigError("load error"))
		})
	service := ratelimit.NewService(t.runtime, t.cache, t.configLoader, t.statStore, true, nil)

	request := common.NewRateLimitRequest("test-domain", [][][2]string{{{"hello", "world"}}}, 1)
	response, err := service.ShouldRateLimit(context.TODO(), request)
	t.assert.Nil(response)
	t.assert.Equal("no rate limit configuration loaded", err.Error())
	t.assert.EqualValues(1, t.statStore.NewCounter("call.should_rate_limit.service_error").Value())
}

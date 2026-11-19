package limiter

import (
	"math/rand"
	"testing"

	mockstats "github.com/envoyproxy/ratelimit/test/mocks/stats"

	"github.com/coocood/freecache"
	pb "github.com/envoyproxy/go-control-plane/envoy/service/ratelimit/v3"
	"github.com/golang/mock/gomock"
	stats "github.com/lyft/gostats"
	"github.com/stretchr/testify/assert"

	"github.com/envoyproxy/ratelimit/src/config"
	"github.com/envoyproxy/ratelimit/src/limiter"
	"github.com/envoyproxy/ratelimit/test/common"
	mock_utils "github.com/envoyproxy/ratelimit/test/mocks/utils"
)

func TestGenerateCacheKeys(t *testing.T) {
	assert := assert.New(t)
	controller := gomock.NewController(t)
	defer controller.Finish()
	timeSource := mock_utils.NewMockTimeSource(controller)
	jitterSource := mock_utils.NewMockJitterRandSource(controller)
	statsStore := stats.NewStore(stats.NewNullSink(), false)
	sm := mockstats.NewMockStatManager(statsStore)
	timeSource.EXPECT().UnixNow().Return(int64(1234))
	baseRateLimit := limiter.NewBaseRateLimit(timeSource, rand.New(jitterSource), 3600, nil, 0.8, "", sm)
	request := common.NewRateLimitRequest("domain", [][][2]string{{{"key", "value"}}}, 1)
	limits := []*config.RateLimit{config.NewRateLimit(10, pb.RateLimitResponse_RateLimit_SECOND, sm.NewStats("key_value"), false, false, "", nil, false)}
	assert.Equal(uint64(0), limits[0].Stats.TotalHits.Value())
	cacheKeys := baseRateLimit.GenerateCacheKeys(request, limits, 1)
	assert.Equal(1, len(cacheKeys))
	assert.Equal("domain_key_value_1234", cacheKeys[0].Key)
	assert.Equal(uint64(1), limits[0].Stats.TotalHits.Value())
}

func TestGenerateCacheKeysPrefix(t *testing.T) {
	assert := assert.New(t)
	controller := gomock.NewController(t)
	defer controller.Finish()
	timeSource := mock_utils.NewMockTimeSource(controller)
	jitterSource := mock_utils.NewMockJitterRandSource(controller)
	statsStore := stats.NewStore(stats.NewNullSink(), false)
	sm := mockstats.NewMockStatManager(statsStore)
	timeSource.EXPECT().UnixNow().Return(int64(1234))
	baseRateLimit := limiter.NewBaseRateLimit(timeSource, rand.New(jitterSource), 3600, nil, 0.8, "prefix:", sm)
	request := common.NewRateLimitRequest("domain", [][][2]string{{{"key", "value"}}}, 1)
	limits := []*config.RateLimit{config.NewRateLimit(10, pb.RateLimitResponse_RateLimit_SECOND, sm.NewStats("key_value"), false, false, "", nil, false)}
	assert.Equal(uint64(0), limits[0].Stats.TotalHits.Value())
	cacheKeys := baseRateLimit.GenerateCacheKeys(request, limits, 1)
	assert.Equal(1, len(cacheKeys))
	assert.Equal("prefix:domain_key_value_1234", cacheKeys[0].Key)
	assert.Equal(uint64(1), limits[0].Stats.TotalHits.Value())
}

func TestOverLimitWithLocalCache(t *testing.T) {
	assert := assert.New(t)
	controller := gomock.NewController(t)
	defer controller.Finish()
	localCache := freecache.NewCache(100)
	localCache.Set([]byte("key"), []byte("value"), 100)
	sm := mockstats.NewMockStatManager(stats.NewStore(stats.NewNullSink(), false))
	baseRateLimit := limiter.NewBaseRateLimit(nil, nil, 3600, localCache, 0.8, "", sm)
	// Returns true, as local cache contains over limit value for the key.
	assert.Equal(true, baseRateLimit.IsOverLimitWithLocalCache("key"))
}

func TestNoOverLimitWithLocalCache(t *testing.T) {
	assert := assert.New(t)
	controller := gomock.NewController(t)
	defer controller.Finish()
	sm := mockstats.NewMockStatManager(stats.NewStore(stats.NewNullSink(), false))
	baseRateLimit := limiter.NewBaseRateLimit(nil, nil, 3600, nil, 0.8, "", sm)
	// Returns false, as local cache is nil.
	assert.Equal(false, baseRateLimit.IsOverLimitWithLocalCache("domain_key_value_1234"))
	localCache := freecache.NewCache(100)
	baseRateLimitWithLocalCache := limiter.NewBaseRateLimit(nil, nil, 3600, localCache, 0.8, "", sm)
	// Returns false, as local cache does not contain value for cache key.
	assert.Equal(false, baseRateLimitWithLocalCache.IsOverLimitWithLocalCache("domain_key_value_1234"))
}

func TestGetResponseStatusEmptyKey(t *testing.T) {
	assert := assert.New(t)
	controller := gomock.NewController(t)
	defer controller.Finish()
	sm := mockstats.NewMockStatManager(stats.NewStore(stats.NewNullSink(), false))
	baseRateLimit := limiter.NewBaseRateLimit(nil, nil, 3600, nil, 0.8, "", sm)
	responseStatus := baseRateLimit.GetResponseDescriptorStatus("", nil, false, 1)
	assert.Equal(pb.RateLimitResponse_OK, responseStatus.GetCode())
	assert.Equal(uint32(0), responseStatus.GetLimitRemaining())
}

func TestGetResponseStatusOverLimitWithLocalCache(t *testing.T) {
	assert := assert.New(t)
	controller := gomock.NewController(t)
	defer controller.Finish()
	timeSource := mock_utils.NewMockTimeSource(controller)
	timeSource.EXPECT().UnixNow().Return(int64(1234))
	statsStore := stats.NewStore(stats.NewNullSink(), false)
	sm := mockstats.NewMockStatManager(statsStore)
	baseRateLimit := limiter.NewBaseRateLimit(timeSource, nil, 3600, nil, 0.8, "", sm)
	limits := []*config.RateLimit{config.NewRateLimit(5, pb.RateLimitResponse_RateLimit_SECOND, sm.NewStats("key_value"), false, false, "", nil, false)}
	limitInfo := limiter.NewRateLimitInfo(limits[0], 2, 6, 4, 5)
	// As `isOverLimitWithLocalCache` is passed as `true`, immediate response is returned with no checks of the limits.
	responseStatus := baseRateLimit.GetResponseDescriptorStatus("key", limitInfo, true, 2)
	assert.Equal(pb.RateLimitResponse_OVER_LIMIT, responseStatus.GetCode())
	assert.Equal(uint32(0), responseStatus.GetLimitRemaining())
	assert.Equal(limits[0].Limit, responseStatus.GetCurrentLimit())
	assert.Equal(uint64(2), limits[0].Stats.OverLimit.Value())
	assert.Equal(uint64(2), limits[0].Stats.OverLimitWithLocalCache.Value())
	// No shadow_mode so, no stats change
	assert.Equal(uint64(0), limits[0].Stats.ShadowMode.Value())
}

func TestGetResponseStatusOverLimitWithLocalCacheShadowMode(t *testing.T) {
	assert := assert.New(t)
	controller := gomock.NewController(t)
	defer controller.Finish()
	timeSource := mock_utils.NewMockTimeSource(controller)
	timeSource.EXPECT().UnixNow().Return(int64(1234))
	statsStore := stats.NewStore(stats.NewNullSink(), false)
	sm := mockstats.NewMockStatManager(statsStore)
	baseRateLimit := limiter.NewBaseRateLimit(timeSource, nil, 3600, nil, 0.8, "", sm)
	// This limit is in ShadowMode
	limits := []*config.RateLimit{config.NewRateLimit(5, pb.RateLimitResponse_RateLimit_SECOND, sm.NewStats("key_value"), false, true, "", nil, false)}
	limitInfo := limiter.NewRateLimitInfo(limits[0], 2, 6, 4, 5)
	// As `isOverLimitWithLocalCache` is passed as `true`, immediate response is returned with no checks of the limits.
	responseStatus := baseRateLimit.GetResponseDescriptorStatus("key", limitInfo, true, 2)
	// Limit is reached, but response is still OK due to ShadowMode
	assert.Equal(pb.RateLimitResponse_OK, responseStatus.GetCode())
	assert.Equal(uint32(0), responseStatus.GetLimitRemaining())
	assert.Equal(limits[0].Limit, responseStatus.GetCurrentLimit())
	assert.Equal(uint64(2), limits[0].Stats.OverLimit.Value())
	// ShadowMode statistics should also be updated
	assert.Equal(uint64(2), limits[0].Stats.ShadowMode.Value())
	assert.Equal(uint64(2), limits[0].Stats.OverLimitWithLocalCache.Value())
}

func TestGetResponseStatusOverLimit(t *testing.T) {
	assert := assert.New(t)
	controller := gomock.NewController(t)
	defer controller.Finish()
	timeSource := mock_utils.NewMockTimeSource(controller)
	timeSource.EXPECT().UnixNow().Return(int64(1234))
	statsStore := stats.NewStore(stats.NewNullSink(), false)
	localCache := freecache.NewCache(100)
	sm := mockstats.NewMockStatManager(statsStore)
	baseRateLimit := limiter.NewBaseRateLimit(timeSource, nil, 3600, localCache, 0.8, "", sm)
	limits := []*config.RateLimit{config.NewRateLimit(5, pb.RateLimitResponse_RateLimit_SECOND, sm.NewStats("key_value"), false, false, "", nil, false)}
	limitInfo := limiter.NewRateLimitInfo(limits[0], 2, 7, 4, 5)
	responseStatus := baseRateLimit.GetResponseDescriptorStatus("key", limitInfo, false, 1)
	assert.Equal(pb.RateLimitResponse_OVER_LIMIT, responseStatus.GetCode())
	assert.Equal(uint32(0), responseStatus.GetLimitRemaining())
	assert.Equal(limits[0].Limit, responseStatus.GetCurrentLimit())
	result, _ := localCache.Get([]byte("key"))
	// Local cache should have been populated with over the limit key.
	assert.Equal("", string(result))
	assert.Equal(uint64(2), limits[0].Stats.OverLimit.Value())
	assert.Equal(uint64(1), limits[0].Stats.NearLimit.Value())
	// No shadow_mode so, no stats change
	assert.Equal(uint64(0), limits[0].Stats.ShadowMode.Value())
}

func TestGetResponseStatusOverLimitShadowMode(t *testing.T) {
	assert := assert.New(t)
	controller := gomock.NewController(t)
	defer controller.Finish()
	timeSource := mock_utils.NewMockTimeSource(controller)
	timeSource.EXPECT().UnixNow().Return(int64(1234))
	statsStore := stats.NewStore(stats.NewNullSink(), false)
	localCache := freecache.NewCache(100)
	sm := mockstats.NewMockStatManager(statsStore)
	baseRateLimit := limiter.NewBaseRateLimit(timeSource, nil, 3600, localCache, 0.8, "", sm)
	// Key is in shadow_mode: true
	limits := []*config.RateLimit{config.NewRateLimit(5, pb.RateLimitResponse_RateLimit_SECOND, sm.NewStats("key_value"), false, true, "", nil, false)}
	limitInfo := limiter.NewRateLimitInfo(limits[0], 2, 7, 4, 5)
	responseStatus := baseRateLimit.GetResponseDescriptorStatus("key", limitInfo, false, 1)
	assert.Equal(pb.RateLimitResponse_OK, responseStatus.GetCode())
	assert.Equal(uint32(0), responseStatus.GetLimitRemaining())
	assert.Equal(limits[0].Limit, responseStatus.GetCurrentLimit())
	result, _ := localCache.Get([]byte("key"))
	// Local cache should have been populated with over the limit key.
	assert.Equal("", string(result))
	assert.Equal(uint64(2), limits[0].Stats.OverLimit.Value())
	assert.Equal(uint64(1), limits[0].Stats.NearLimit.Value())
}

func TestGetResponseStatusBelowLimit(t *testing.T) {
	assert := assert.New(t)
	controller := gomock.NewController(t)
	defer controller.Finish()
	timeSource := mock_utils.NewMockTimeSource(controller)
	timeSource.EXPECT().UnixNow().Return(int64(1234))
	statsStore := stats.NewStore(stats.NewNullSink(), false)
	sm := mockstats.NewMockStatManager(statsStore)
	baseRateLimit := limiter.NewBaseRateLimit(timeSource, nil, 3600, nil, 0.8, "", sm)
	limits := []*config.RateLimit{config.NewRateLimit(10, pb.RateLimitResponse_RateLimit_SECOND, sm.NewStats("key_value"), false, false, "", nil, false)}
	limitInfo := limiter.NewRateLimitInfo(limits[0], 2, 6, 9, 10)
	responseStatus := baseRateLimit.GetResponseDescriptorStatus("key", limitInfo, false, 1)
	assert.Equal(pb.RateLimitResponse_OK, responseStatus.GetCode())
	assert.Equal(uint32(4), responseStatus.GetLimitRemaining())
	assert.Equal(uint64(0), limits[0].Stats.NearLimit.Value())
	assert.Equal(limits[0].Limit, responseStatus.GetCurrentLimit())
	assert.Equal(uint64(1), limits[0].Stats.WithinLimit.Value())
	// No shadow_mode so, no stats change
	assert.Equal(uint64(0), limits[0].Stats.ShadowMode.Value())
}

func TestGetResponseStatusBelowLimitShadowMode(t *testing.T) {
	assert := assert.New(t)
	controller := gomock.NewController(t)
	defer controller.Finish()
	timeSource := mock_utils.NewMockTimeSource(controller)
	timeSource.EXPECT().UnixNow().Return(int64(1234))
	statsStore := stats.NewStore(stats.NewNullSink(), false)
	sm := mockstats.NewMockStatManager(statsStore)
	baseRateLimit := limiter.NewBaseRateLimit(timeSource, nil, 3600, nil, 0.8, "", sm)
	limits := []*config.RateLimit{config.NewRateLimit(10, pb.RateLimitResponse_RateLimit_SECOND, sm.NewStats("key_value"), false, true, "", nil, false)}
	limitInfo := limiter.NewRateLimitInfo(limits[0], 2, 6, 9, 10)
	responseStatus := baseRateLimit.GetResponseDescriptorStatus("key", limitInfo, false, 1)
	assert.Equal(pb.RateLimitResponse_OK, responseStatus.GetCode())
	assert.Equal(uint32(4), responseStatus.GetLimitRemaining())
	assert.Equal(uint64(0), limits[0].Stats.NearLimit.Value())
	assert.Equal(limits[0].Limit, responseStatus.GetCurrentLimit())
	assert.Equal(uint64(1), limits[0].Stats.WithinLimit.Value())
	// No shadow_mode so, no stats change
	assert.Equal(uint64(0), limits[0].Stats.ShadowMode.Value())
}

func utilizationCounts(rl *config.RateLimit) map[uint32]uint64 {
	counts := map[uint32]uint64{}
	for _, bucket := range rl.Stats.Utilization {
		counts[bucket.MaxPercent] = bucket.Counter.Value()
	}
	return counts
}

func newUtilizationTestLimit(t *testing.T, requestsPerUnit uint32) (*limiter.BaseRateLimiter, *config.RateLimit) {
	timeSource := mock_utils.NewMockTimeSource(gomock.NewController(t))
	timeSource.EXPECT().UnixNow().Return(int64(1234)).AnyTimes()
	sm := mockstats.NewMockStatManager(stats.NewStore(stats.NewNullSink(), false))
	baseRateLimit := limiter.NewBaseRateLimit(timeSource, nil, 3600, nil, 0.8, "", sm)
	rl := config.NewRateLimit(requestsPerUnit, pb.RateLimitResponse_RateLimit_MINUTE, sm.NewStats("key_value"), false, false, "", nil, false)
	return baseRateLimit, rl
}

func TestUtilizationBucketsCountHitsWithinLimit(t *testing.T) {
	baseRateLimit, rl := newUtilizationTestLimit(t, 10)

	// 6 of 10 used after this hit: 60%
	baseRateLimit.GetResponseDescriptorStatus("key", limiter.NewRateLimitInfo(rl, 5, 6, 0, 0), false, 1)

	assert.Equal(t, map[uint32]uint64{10: 0, 25: 0, 50: 0, 75: 1, 90: 1, 100: 1}, utilizationCounts(rl))
}

func TestUtilizationBucketsCountTheLastHitUnderTheLimitAt100(t *testing.T) {
	baseRateLimit, rl := newUtilizationTestLimit(t, 10)

	baseRateLimit.GetResponseDescriptorStatus("key", limiter.NewRateLimitInfo(rl, 9, 10, 0, 0), false, 1)

	assert.Equal(t, map[uint32]uint64{10: 0, 25: 0, 50: 0, 75: 0, 90: 0, 100: 1}, utilizationCounts(rl))
}

func TestUtilizationBucketsSkipHitsOverTheLimit(t *testing.T) {
	baseRateLimit, rl := newUtilizationTestLimit(t, 5)

	baseRateLimit.GetResponseDescriptorStatus("key", limiter.NewRateLimitInfo(rl, 5, 6, 0, 0), false, 1)

	assert.Equal(t, map[uint32]uint64{10: 0, 25: 0, 50: 0, 75: 0, 90: 0, 100: 0}, utilizationCounts(rl))
	assert.Equal(t, uint64(1), rl.Stats.OverLimit.Value())
}

func TestUtilizationBucketsAddEveryHitOfAMultiHitRequest(t *testing.T) {
	baseRateLimit, rl := newUtilizationTestLimit(t, 100)

	baseRateLimit.GetResponseDescriptorStatus("key", limiter.NewRateLimitInfo(rl, 0, 3, 0, 0), false, 3)

	assert.Equal(t, uint64(3), utilizationCounts(rl)[10])
}

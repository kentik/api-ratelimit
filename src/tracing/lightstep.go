package tracing

import (
	"google.golang.org/grpc"
	"net/http"
	"os"
	"reflect"
	"time"

	"context"

	grpcopentracing "github.com/grpc-ecosystem/go-grpc-middleware/tracing/opentracing"
	"github.com/lightstep/lightstep-tracer-go"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/opentracing/opentracing-go/log"
)

const (
	LightstepLoggingPrefix = "lightstep"

	EnableLightstepTracingEnvVar  = "K_TRACING_LIGHTSTEP_ENABLED"
	EnableLightstepTracingDefault = false
	LightstepTracingTokenEnvVar   = "K_TRACING_LIGHTSTEP_TOKEN"
	LightstepTracingTokenDefault  = ""
	LightstepTracingHostEnvVar    = "K_TRACING_LIGHTSTEP_HOST"
	LightstepTracingHostDefault   = ""
	LightstepTracingPortEnvVar    = "K_TRACING_LIGHTSTEP_PORT"
	LightstepTracingPortDefault   = 0
)
const (
	JsonMarshalOp   = "json.Marshal"
	JsonUnmarshalOp = "json.Unmarshal"
)

type LightstepConfig struct {
	Enabled bool
	Host    string
	Port    int
	Token   string
}

func GetLightstepConfigFromEnv() LightstepConfig {
	return LightstepConfig{
		Enabled: ParseBoolDefaultOrPanic(os.Getenv(EnableLightstepTracingEnvVar), EnableLightstepTracingDefault),
		Host:    StrOrDef(os.Getenv(LightstepTracingHostEnvVar), LightstepTracingHostDefault),
		Port:    AtoiDefaultOrPanic(os.Getenv(LightstepTracingPortEnvVar), LightstepTracingPortDefault),
		Token:   StrOrDef(os.Getenv(LightstepTracingTokenEnvVar), LightstepTracingTokenDefault),
	}
}

type LightstepTracer struct {
	lightstep.Tracer
	Enabled bool
	LightstepShutdownTimeout time.Duration
}

func NewLightstepTracer(config LightstepConfig, version string) *LightstepTracer {
	if !config.Enabled {
		//l.Infof("Lightstep tracing disabled (continuing without tracing)")
		return &LightstepTracer{}
	}

	lightstepTracer := lightstep.NewTracer(lightstep.Options{
		Collector: lightstep.Endpoint{
			Host:      config.Host,
			Port:      config.Port,
			Plaintext: true,
		},
		UseHttp:     false,
		UseGRPC:     true,
		AccessToken: config.Token,
		Propagators: map[opentracing.BuiltinFormat]lightstep.Propagator{
			opentracing.HTTPHeaders: lightstep.B3Propagator,
			opentracing.TextMap:     lightstep.B3Propagator,
		},
		Tags: map[string]interface{}{
			lightstep.ComponentNameKey: "syn-back",
			"service.version":          version,
		},
	})
	if lightstepTracer == nil {
		//l.Errorf("Lightstep tracing disabled: could not create lightstep tracer (continuing without tracing)")
		return &LightstepTracer{}
	}
	opentracing.SetGlobalTracer(lightstepTracer)

	//l.Infof("Lightstep tracing enabled")
	return &LightstepTracer{
		Tracer:                   lightstepTracer,
		Enabled:                  true,
		//ContextL:                 l,
		LightstepShutdownTimeout: time.Second,
	}
}

func (t *LightstepTracer) Close() {
	if !t.Enabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.LightstepShutdownTimeout)
	//t.Infof("Lightstep tracing shutting down (timeout %s)", t.LightstepShutdownTimeout)
	t.Tracer.Close(ctx)
	cancel()
}

func NewOpentracingGRPCGatewayMiddleware() func(http.Handler) http.Handler {
	if !opentracing.IsGlobalTracerRegistered() {
		return func(handler http.Handler) http.Handler { return handler }
	}
	return func(handler http.Handler) http.Handler {
		return &OpentracingGRPCGatewayMiddleware{
			Tracer:  opentracing.GlobalTracer(),
			Handler: handler,
		}
	}
}

type OpentracingGRPCGatewayMiddleware struct {
	Tracer  opentracing.Tracer
	Handler http.Handler
}

func (h *OpentracingGRPCGatewayMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parentSpanContext, err := h.Tracer.Extract(
		opentracing.HTTPHeaders,
		opentracing.HTTPHeadersCarrier(r.Header))
	if err == nil || err == opentracing.ErrSpanContextNotFound {
		serverSpan := h.Tracer.StartSpan(
			"ServeHTTP",
			// this is magical, it attaches the new span to the parent parentSpanContext, and creates an unparented one if empty.
			ext.RPCServerOption(parentSpanContext),
			grpcGatewayTag,
		)
		r = r.WithContext(opentracing.ContextWithSpan(r.Context(), serverSpan))
		defer serverSpan.Finish()
	}
	h.Handler.ServeHTTP(w, r)
}

var grpcGatewayTag = opentracing.Tag{Key: string(ext.Component), Value: "grpc-gateway"}

func NewOpentracingGRPCClientInterceptorOptions() []grpc.DialOption {
	if opentracing.IsGlobalTracerRegistered() {
		return []grpc.DialOption{grpc.WithUnaryInterceptor(grpcopentracing.UnaryClientInterceptor())}
	}
	return nil
}

func NewOpentracingGRPCServerInterceptors() []grpc.UnaryServerInterceptor {
	if opentracing.IsGlobalTracerRegistered() {
		return []grpc.UnaryServerInterceptor{
			grpcopentracing.UnaryServerInterceptor(),
		}
	}
	return nil
}

func tracerOrGlobal(tracer opentracing.Tracer) opentracing.Tracer {
	if tracer != nil {
		return tracer
	}
	return opentracing.GlobalTracer()
}

var noopTracer = opentracing.NoopTracer{}
var noopSpan = noopTracer.StartSpan("noop")

func OrNoop(tracer opentracing.Tracer, span opentracing.Span) (opentracing.Tracer, opentracing.Span) {
	if tracer == nil {
		tracer = noopTracer
	}
	if span == nil {
		span = noopSpan
	}
	return tracer, span
}

func StartSpan(tracer opentracing.Tracer, parent opentracing.Span, op string) opentracing.Span {
	tracer = tracerOrGlobal(tracer)
	if parent == nil {
		return tracer.StartSpan(op)
	} else {
		return tracer.StartSpan(op, opentracing.ChildOf(parent.Context()))
	}
}

func StartClientSpan(tracer opentracing.Tracer, parent opentracing.Span, op string, method string, url string, body []byte) opentracing.Span {
	tracer = tracerOrGlobal(tracer)
	var span opentracing.Span
	if parent == nil {
		span = tracer.StartSpan(op)
	} else {
		span = tracer.StartSpan(op, opentracing.ChildOf(parent.Context()))
	}
	ext.SpanKindRPCClient.Set(span)
	ext.HTTPUrl.Set(span, url)
	ext.HTTPMethod.Set(span, method)
	span.SetTag("req_body_len", len(body))
	span.LogFields(log.Int("req_body_len", len(body)))
	return span
}

func PrepareRequest(tracer opentracing.Tracer, span opentracing.Span, req *http.Request) {
	if span == nil || req == nil {
		return
	}
	tracer = tracerOrGlobal(tracer)
	_ = tracer.Inject(span.Context(), opentracing.HTTPHeaders, opentracing.HTTPHeadersCarrier(req.Header))
}

func RecordError(span opentracing.Span, err error) {
	if span == nil || err == nil {
		return
	}
	ext.Error.Set(span, true)
	span.LogFields(log.String("error.msg", err.Error()))
	span.LogFields(log.String("error.type", reflect.TypeOf(err).Name()))
}

func RecordErrorAndFinish(span opentracing.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.Finish()
}

func RecordResponse(span opentracing.Span, resp *http.Response) {
	if span == nil || resp == nil {
		return
	}
	ext.HTTPStatusCode.Set(span, uint16(resp.StatusCode))
	span.LogFields(log.Int64("resp_body_len", resp.ContentLength))
}

func RecordResponseAndFinish(span opentracing.Span, resp *http.Response) {
	if span == nil || resp == nil {
		return
	}
	RecordResponse(span, resp)
	span.Finish()
}

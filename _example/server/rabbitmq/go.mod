module github.com/tx7do/kratos-transport/_example/server/rabbitmq

go 1.19

replace google.golang.org/grpc => google.golang.org/grpc v1.46.2

require (
	github.com/go-kratos/kratos/v2 v2.6.2
	github.com/tx7do/kratos-transport v1.0.6
	github.com/tx7do/kratos-transport/broker/rabbitmq v0.0.0-20230620105535-5e89f29faa3d
	github.com/tx7do/kratos-transport/transport/rabbitmq v0.0.0-20230620102913-29fa3fb6e659
)

require (
	github.com/go-logr/logr v1.2.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-playground/form/v4 v4.2.0 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/openzipkin/zipkin-go v0.4.1 // indirect
	github.com/rabbitmq/amqp091-go v1.8.1 // indirect
	go.opentelemetry.io/otel v1.16.0 // indirect
	go.opentelemetry.io/otel/exporters/jaeger v1.16.0 // indirect
	go.opentelemetry.io/otel/exporters/zipkin v1.16.0 // indirect
	go.opentelemetry.io/otel/metric v1.16.0 // indirect
	go.opentelemetry.io/otel/sdk v1.16.0 // indirect
	go.opentelemetry.io/otel/trace v1.16.0 // indirect
	golang.org/x/net v0.8.0 // indirect
	golang.org/x/sync v0.3.0 // indirect
	golang.org/x/sys v0.9.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20230530153820-e85fd2cbaebc // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20230530153820-e85fd2cbaebc // indirect
	google.golang.org/grpc v1.56.0 // indirect
	google.golang.org/protobuf v1.30.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

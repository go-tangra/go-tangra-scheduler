module github.com/go-tangra/go-tangra-scheduler/v4

go 1.26.3

toolchain go1.26.8

require (
	github.com/go-tangra/go-tangra-scheduler/sdk/v4 v4.0.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
)

require golang.org/x/text v0.42.0 // indirect

replace github.com/go-tangra/go-tangra-scheduler/sdk/v4 => ./sdk

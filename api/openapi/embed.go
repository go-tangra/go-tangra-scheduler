// Package openapi embeds the scheduler browser API contract.
package openapi

import _ "embed"

// Scheduler is the OpenAPI 3.1 document served and validated by the service.
//
//go:embed scheduler.yaml
var Scheduler []byte

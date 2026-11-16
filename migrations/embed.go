package migrations

import _ "embed"

//go:embed 001_init.sql
var Initial string

//go:embed 002_observability.sql
var Observability string

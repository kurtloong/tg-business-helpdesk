package migrations

import _ "embed"

//go:embed 001_init.sql
var Initial string

//go:embed 002_observability.sql
var Observability string

//go:embed 003_direct_bot.sql
var BotRoutes string

//go:embed 004_customer_groups.sql
var CustomerGroups string

// SourceAnalyser
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

module tachyonik/sourceanalyser

go 1.24.4

require (
	github.com/dop251/goja v0.0.0-20260216154549-8b74ce4618c5
	gopkg.in/yaml.v3 v3.0.1
	tachyonik/lib v1.0.0
)

replace tachyonik/lib => ../TachyonikLib

require (
	github.com/dlclark/regexp2 v1.11.4 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20230207041349-798e818bf904 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/ledongthuc/pdf v0.0.0-20250511090121-5959a4027728 // indirect
	golang.org/x/text v0.3.8 // indirect
)

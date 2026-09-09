package main

import _ "embed"

//go:embed panel_style.html
var panelStyle string

//go:embed panel_adaptive_style.html
var panelAdaptiveStyle string

//go:embed panel_quota_runtime_style.html
var panelQuotaRuntimeStyle string

//go:embed panel_markup.html
var panelMarkup string

//go:embed panel_script_1.js
var panelScript1 string

//go:embed panel_script_2.js
var panelScript2 string

//go:embed panel_script_3.js
var panelScript3 string

//go:embed panel_script_4.js
var panelScript4 string

//go:embed panel_script_5.js
var panelScript5 string

//go:embed panel_tail.html
var panelTail string

var panelHTML = panelStyle + panelAdaptiveStyle + panelQuotaRuntimeStyle + panelMarkup + panelScript1 + panelScript2 + panelScript3 + panelScript4 + panelScript5 + panelTail

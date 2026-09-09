package main

import _ "embed"

//go:embed panel_style.html
var panelStyle string

//go:embed panel_adaptive_style.html
var panelAdaptiveStyle string

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

//go:embed panel_tail.html
var panelTail string

var panelHTML = panelStyle + panelAdaptiveStyle + panelMarkup + panelScript1 + panelScript2 + panelScript3 + panelScript4 + panelTail

package web

import _ "embed"

//go:embed index.html
var Index []byte

//go:embed login.html
var Login []byte

package web

import _ "embed"

//go:embed index.html
var indexHTML string

//go:embed login.html
var loginHTML string

func getIndexHTML() string {
	return indexHTML
}

func getLoginHTML() string {
	return loginHTML
}

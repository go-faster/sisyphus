package fetch

import "net/http"

type credentialApplier interface {
	apply(req *http.Request)
	headerName() string
}

type bearerCred struct{ token string }

func (c bearerCred) apply(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
}

func (bearerCred) headerName() string { return "Authorization" }

type basicCred struct{ user, pass string }

func (c basicCred) apply(req *http.Request) {
	req.SetBasicAuth(c.user, c.pass)
}

func (basicCred) headerName() string { return "Authorization" }

type headerCred struct{ header, value string }

func (c headerCred) apply(req *http.Request) {
	req.Header.Set(c.header, c.value)
}

func (c headerCred) headerName() string { return c.header }

type noneCred struct{}

func (noneCred) apply(*http.Request) {}

func (noneCred) headerName() string { return "" }

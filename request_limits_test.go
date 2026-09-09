package main

import (
	"testing"

	"github.com/valyala/fasthttp"
)

func TestRequestReadConfigKeepsDefaultDeadlineForOrdinaryRequests(t *testing.T) {
	header := &fasthttp.RequestHeader{}
	header.SetMethod("GET")
	header.SetRequestURI("/api/health")

	config := requestReadConfig(header)
	if config.ReadTimeout != backendReadTimeout {
		t.Fatalf("ordinary request timeout = %s, want %s", config.ReadTimeout, backendReadTimeout)
	}
}

func TestRequestReadConfigRecognizesStudioUploadWithQuery(t *testing.T) {
	header := &fasthttp.RequestHeader{}
	header.SetMethod("POST")
	header.SetRequestURI(studioSourceUploadPath + "?source=studio")

	config := requestReadConfig(header)
	if config.ReadTimeout <= backendReadTimeout {
		t.Fatalf("Studio upload timeout = %s, want a bounded body budget above %s", config.ReadTimeout, backendReadTimeout)
	}
}

func TestStudioUploadReadTimeoutScalesWithDeclaredBodySizeAndIsCapped(t *testing.T) {
	small := studioUploadReadTimeout(1)
	hardCandidate := studioUploadReadTimeout(13_015_175)
	large := studioUploadReadTimeout(100 * 1024 * 1024)
	unknown := studioUploadReadTimeout(-1)

	if small != backendReadTimeout {
		t.Fatalf("small upload timeout = %s, want default floor %s", small, backendReadTimeout)
	}
	if hardCandidate <= backendReadTimeout {
		t.Fatalf("hard candidate timeout = %s, want above default %s", hardCandidate, backendReadTimeout)
	}
	if hardCandidate >= large {
		t.Fatalf("hard candidate timeout = %s, want less than 100 MiB timeout %s", hardCandidate, large)
	}
	if large > studioUploadReadTimeoutCeiling || unknown != studioUploadReadTimeoutCeiling {
		t.Fatalf("upload timeout cap not enforced: large=%s unknown=%s cap=%s", large, unknown, studioUploadReadTimeoutCeiling)
	}
}

func TestIsStudioSourceUploadRejectsOtherRoutesAndMethods(t *testing.T) {
	cases := []struct {
		name   string
		method string
		uri    string
		want   bool
	}{
		{name: "other method", method: "GET", uri: studioSourceUploadPath, want: false},
		{name: "other path", method: "POST", uri: "/api/studio/v1/sessions", want: false},
		{name: "near miss", method: "POST", uri: studioSourceUploadPath + "/extra", want: false},
		{name: "exact path", method: "POST", uri: studioSourceUploadPath, want: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			header := &fasthttp.RequestHeader{}
			header.SetMethod(testCase.method)
			header.SetRequestURI(testCase.uri)
			if got := isStudioSourceUpload(header); got != testCase.want {
				t.Fatalf("isStudioSourceUpload() = %t, want %t", got, testCase.want)
			}
		})
	}
}

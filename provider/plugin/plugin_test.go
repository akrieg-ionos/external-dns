/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package plugin

import (
	"bytes"
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"testing"
)

type whenRequest struct {
	method  string
	path    string
	headers map[string]string
	payload string
}

type thenResponse struct {
	statusCode int
	headers    map[string]string
	payload    string
}

func createTestServer(t *testing.T, when whenRequest, then thenResponse) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, when.method, r.Method, "method")
		require.Equal(t, when.path, r.URL.Path, "path")
		for k, v := range when.headers {
			require.Equal(t, v, r.Header.Get(k), "header %s", k)
		}
		//if when.payload != "" {
		// get the request body
		requestBody := new(bytes.Buffer)
		_, err := requestBody.ReadFrom(r.Body)
		require.NoError(t, err, "reading request body")
		require.Equal(t, when.payload, requestBody.String(), "request payload")
		//}
		w.WriteHeader(then.statusCode)
		w.Write([]byte(then.payload))

	}))
}

func TestMain(m *testing.M) {
	log.SetFormatter(&log.TextFormatter{})
	log.SetLevel(log.DebugLevel)
	m.Run()
}

func TestRecords(t *testing.T) {
	testCases := []struct {
		name                   string
		whenResponsePayload    string
		whenResponseStatusCode int
		thenRecords            []*endpoint.Endpoint
		thenError              string
	}{
		{
			name:                   "no records",
			whenResponsePayload:    `[]`,
			whenResponseStatusCode: http.StatusOK,
			thenRecords:            []*endpoint.Endpoint{},
		},
		{
			name:                   "one record",
			whenResponsePayload:    `[{ "dnsName" : "test.example.com" }]`,
			whenResponseStatusCode: http.StatusOK,
			thenRecords:            []*endpoint.Endpoint{{DNSName: "test.example.com"}},
		},
		{
			name:                   "multiple records",
			whenResponsePayload:    `[{ "dnsName" : "test.example.com" }, { "dnsName" : "test2.example.com" }]`,
			whenResponseStatusCode: http.StatusOK,
			thenRecords:            []*endpoint.Endpoint{{DNSName: "test.example.com"}, {DNSName: "test2.example.com"}},
		},
		{
			name: "one record with all attributes",
			whenResponsePayload: `
[
    {
        "dnsName": "aDNSValue",
        "targets": [
            "target1",
            "target2"
        ],
        "recordType": "aRecordType",
        "setIdentifier": "anIdentifier",
        "recordTTL": 3600,
        "labels": {
            "firstLabel": "firstLabelValue",
            "secondLabel": "secondLabelValue"
        },
        "providerSpecific": [
            {
                "name": "name1Value",
                "value": "value1value"
            },
            {
                "name": "name2Value",
                "value": "value2value"
            }
        ]
    }
]`,
			whenResponseStatusCode: http.StatusOK,
			thenRecords: []*endpoint.Endpoint{{
				DNSName:       "aDNSValue",
				Targets:       endpoint.Targets{"target1", "target2"},
				RecordType:    "aRecordType",
				SetIdentifier: "anIdentifier",
				RecordTTL:     3600,
				Labels: map[string]string{
					"firstLabel":  "firstLabelValue",
					"secondLabel": "secondLabelValue",
				},
				ProviderSpecific: endpoint.ProviderSpecific{
					{
						Name:  "name1Value",
						Value: "value1value",
					},
					{
						Name:  "name2Value",
						Value: "value2value",
					},
				},
			}},
		},
		{
			name:                   "wrong json",
			whenResponsePayload:    `[ invalid json`,
			whenResponseStatusCode: http.StatusOK,
			thenError:              "invalid character 'i' looking for beginning of value",
			thenRecords:            nil,
		},
		{
			name:                   "server error",
			whenResponsePayload:    `[ { "dnsName" : "test.example.com" } ]`,
			whenResponseStatusCode: http.StatusInternalServerError,
			thenError:              "failed to get records with code 500",
			thenRecords:            nil,
		},
		{
			name:                   "unknown record attributes",
			whenResponsePayload:    `[{ "unknownattribute" : "a value" }]`,
			whenResponseStatusCode: http.StatusOK,
			thenRecords:            []*endpoint.Endpoint{{}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			svr := createTestServer(t,
				whenRequest{
					method: http.MethodGet,
					path:   "/records",
					headers: map[string]string{
						"Accept": "application/external.dns.plugin+json;version=1",
					}},
				thenResponse{
					statusCode: tc.whenResponseStatusCode,
					payload:    tc.whenResponsePayload,
					headers: map[string]string{
						"Content-Type": "application/external.dns.plugin+json;version=1",
					},
				})

			defer svr.Close()
			pluginProvider, err := NewPluginProvider(svr.URL)
			require.NoError(t, err)
			records, err := pluginProvider.Records(context.TODO())
			if tc.thenError != "" {
				if err == nil {
					require.Fail(t, "expected error but got none, expected:"+tc.thenError)
				}
				require.Equal(t, tc.thenError, err.Error())
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.thenRecords, records)
		})
	}
}

func TestApplyChanges(t *testing.T) {
	testCases := []struct {
		name               string
		whenApplyChanges   *plan.Changes
		whenStatusCode     int
		thenRequestPayload string
		thenError          string
	}{
		{
			name:               "no changes",
			whenApplyChanges:   &plan.Changes{},
			whenStatusCode:     http.StatusNoContent,
			thenRequestPayload: `{"Create":null,"UpdateOld":null,"UpdateNew":null,"Delete":null}`,
		},
		{
			name: "one creation",
			whenApplyChanges: &plan.Changes{
				Create: []*endpoint.Endpoint{{DNSName: "test.example.com"}},
			},
			whenStatusCode:     http.StatusNoContent,
			thenRequestPayload: `{"Create":[{"dnsName":"test.example.com"}],"UpdateOld":null,"UpdateNew":null,"Delete":null}`,
		},
		{
			name: "one deletion",
			whenApplyChanges: &plan.Changes{
				Delete: []*endpoint.Endpoint{{DNSName: "test.example.com"}},
			},
			whenStatusCode:     http.StatusNoContent,
			thenRequestPayload: `{"Create":null,"UpdateOld":null,"UpdateNew":null,"Delete":[{"dnsName":"test.example.com"}]}`,
		},
		{
			name: "one UpdateNew,UpdateOld",
			whenApplyChanges: &plan.Changes{
				UpdateNew: []*endpoint.Endpoint{{DNSName: "testNew.example.com"}},
				UpdateOld: []*endpoint.Endpoint{{DNSName: "testOld.example.com"}},
			},
			whenStatusCode:     http.StatusNoContent,
			thenRequestPayload: `{"Create":null,"UpdateOld":[{"dnsName":"testOld.example.com"}],"UpdateNew":[{"dnsName":"testNew.example.com"}],"Delete":null}`,
		},
		{
			name: "create with all attributes",
			whenApplyChanges: &plan.Changes{
				Create: []*endpoint.Endpoint{{
					DNSName:       "aDNSValue",
					Targets:       endpoint.Targets{"target1", "target2"},
					RecordType:    "aRecordType",
					SetIdentifier: "anIdentifier",
					RecordTTL:     3600,
					Labels: map[string]string{
						"firstLabel":  "firstLabelValue",
						"secondLabel": "secondLabelValue",
					},
					ProviderSpecific: endpoint.ProviderSpecific{
						{
							Name:  "name1Value",
							Value: "value1value",
						},
						{
							Name:  "name2Value",
							Value: "value2value",
						},
					},
				}},
			},
			whenStatusCode: http.StatusNoContent,
			thenRequestPayload: `{` +
				`"Create":[` +
				`{` +
				`"dnsName":"aDNSValue",` +
				`"targets":["target1","target2"],` +
				`"recordType":"aRecordType",` +
				`"setIdentifier":"anIdentifier",` +
				`"recordTTL":3600,` +
				`"labels":{` +
				`"firstLabel":"firstLabelValue",` +
				`"secondLabel":"secondLabelValue"` +
				`},` +
				`"providerSpecific":[` +
				`{"name":"name1Value","value":"value1value"},` +
				`{"name":"name2Value","value":"value2value"}` +
				`]` +
				`}` +
				`],` +
				`"UpdateOld":null,` +
				`"UpdateNew":null,` +
				`"Delete":null` +
				`}`,
		},
		{
			name:               "server error",
			whenStatusCode:     http.StatusInternalServerError,
			thenError:          "failed to apply changes with code 500",
			thenRequestPayload: "null",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			svr := createTestServer(t,
				whenRequest{
					method: http.MethodPost,
					path:   "/records",
					headers: map[string]string{
						"Content-Type": "application/external.dns.plugin+json;version=1",
					},
					payload: tc.thenRequestPayload,
				},
				thenResponse{
					statusCode: tc.whenStatusCode,
				})
			defer svr.Close()
			pluginProvider, err := NewPluginProvider(svr.URL)
			require.NoError(t, err)
			err = pluginProvider.ApplyChanges(context.Background(), tc.whenApplyChanges)
			if tc.thenError != "" {
				if err == nil {
					require.Fail(t, "expected error but got none, expected:"+tc.thenError)
				}
				require.Equal(t, tc.thenError, err.Error())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPropertyValuesEqual(t *testing.T) {
	testCases := []struct {
		name                   string
		whenResponsePayload    string
		whenResponseStatusCode int
		thenEquals             bool
	}{
		{
			name:                   "equals",
			whenResponsePayload:    `{"equals":true}`,
			whenResponseStatusCode: http.StatusOK,
			thenEquals:             true,
		},
		{
			name:                   "not equals",
			whenResponsePayload:    `{"equals":false}`,
			whenResponseStatusCode: http.StatusOK,
			thenEquals:             false,
		},
		{
			name:                   "invalid json in response returns equals true",
			whenResponsePayload:    `{ invalid`,
			whenResponseStatusCode: http.StatusOK,
			thenEquals:             true,
		},
		{
			name:                   "server error in response status returns equals true",
			whenResponsePayload:    `{"equals":false}`,
			whenResponseStatusCode: http.StatusInternalServerError,
			thenEquals:             true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			propName := "propName"
			previous := "previous"
			current := "current"
			svr := createTestServer(t,
				whenRequest{
					method: http.MethodPost,
					path:   "/propertyvaluesequal",
					headers: map[string]string{
						"Content-Type": "application/external.dns.plugin+json;version=1",
						"Accept":       "application/external.dns.plugin+json;version=1",
					},
					payload: fmt.Sprintf(`{"name":"%s","previous":"%s","current":"%s"}`,
						propName, previous, current),
				},
				thenResponse{
					statusCode: tc.whenResponseStatusCode,
					payload:    tc.whenResponsePayload,
				})
			defer svr.Close()
			pluginProvider, err := NewPluginProvider(svr.URL)
			require.NoError(t, err)
			isEqual := pluginProvider.PropertyValuesEqual(propName, previous, current)
			require.Equal(t, tc.thenEquals, isEqual)
		})
	}
}

func TestAdjustEndpoints(t *testing.T) {
	testCases := []struct {
		name                   string
		whenResponsePayload    string
		whenResponseStatusCode int
		thenEndpoints          []*endpoint.Endpoint
	}{
		{
			name:                   "adjust endpoints",
			whenResponsePayload:    `[{"dnsName":"test.example.com","targets":["target1","target2"]}]`,
			whenResponseStatusCode: http.StatusOK,
			thenEndpoints: []*endpoint.Endpoint{
				{
					DNSName: "test.example.com",
					Targets: endpoint.Targets{"target1", "target2"},
				},
			},
		},
		{
			name:                   "when invalid json response payload, then returns always no endpoints",
			whenResponsePayload:    `[invalid`,
			whenResponseStatusCode: http.StatusOK,
			thenEndpoints:          []*endpoint.Endpoint{},
		},
		{
			name:                   "when unexpected responsestatus, then returns always no endpoints",
			whenResponsePayload:    `[{"dnsName":"test.example.com","targets":["target1","target2"]}]`,
			whenResponseStatusCode: http.StatusInternalServerError,
			thenEndpoints:          []*endpoint.Endpoint{},
		},
	}
	for _, tc := range testCases {
		requestedEndpoints := []*endpoint.Endpoint{
			{
				DNSName: "test.example.com",
			},
		}
		t.Run(tc.name, func(t *testing.T) {
			svr := createTestServer(t,
				whenRequest{
					method: http.MethodPost,
					path:   "/adjustendpoints",
					headers: map[string]string{
						"Content-Type": "application/external.dns.plugin+json;version=1",
						"Accept":       "application/external.dns.plugin+json;version=1",
					},
					payload: `[{"dnsName":"test.example.com"}]`,
				},
				thenResponse{
					statusCode: tc.whenResponseStatusCode,
					payload:    tc.whenResponsePayload,
				})
			defer svr.Close()
			pluginProvider, err := NewPluginProvider(svr.URL)
			require.NoError(t, err)
			endpoints := pluginProvider.AdjustEndpoints(requestedEndpoints)
			require.NoError(t, err)
			require.Equal(t, tc.thenEndpoints, endpoints)
		})
	}

}

//
//func TestAdjustEndpoints(t *testing.T) {
//	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//		if r.URL.Path == "/" {
//			w.Header().Set(varyHeader, contentTypeHeader)
//			w.Header().Set(contentTypeHeader, mediaTypeFormatAndVersion)
//			w.WriteHeader(200)
//			return
//		}
//		var endpoints []*endpoint.Endpoint
//		defer r.Body.Close()
//		b, err := io.ReadAll(r.Body)
//		if err != nil {
//			t.Fatal(err)
//		}
//		err = json.Unmarshal(b, &endpoints)
//		if err != nil {
//			t.Fatal(err)
//		}
//
//		for _, e := range endpoints {
//			e.RecordTTL = 0
//		}
//		j, _ := json.Marshal(endpoints)
//		w.Write(j)
//
//	}))
//	defer svr.Close()
//
//	provider, err := NewPluginProvider(svr.URL)
//	require.Nil(t, err)
//	endpoints := []*endpoint.Endpoint{
//		{
//			DNSName:    "test.example.com",
//			RecordTTL:  10,
//			RecordType: "A",
//			Targets: endpoint.Targets{
//				"",
//			},
//		},
//	}
//	adjustedEndpoints := provider.AdjustEndpoints(endpoints)
//	require.Equal(t, []*endpoint.Endpoint{{
//		DNSName:    "test.example.com",
//		RecordTTL:  0,
//		RecordType: "A",
//		Targets: endpoint.Targets{
//			"",
//		},
//	}}, adjustedEndpoints)

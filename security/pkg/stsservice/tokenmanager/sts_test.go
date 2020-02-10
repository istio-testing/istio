// Copyright 2020 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tokenmanager

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"istio.io/istio/security/pkg/stsservice"
	stsServer "istio.io/istio/security/pkg/stsservice/server"
	"istio.io/istio/security/pkg/stsservice/tokenmanager/google"
	"istio.io/istio/security/pkg/stsservice/tokenmanager/google/mock"
)

// Number of test client to create for testing.
const numClient = 10

var stsServerAddress string

// TestStsService sets up a STS server and token manager which has enabled Google
// token exchange plugin, a mock authorization server for token service, and
// verifies STS flows.
func TestStsFlow(t *testing.T) {
	stsServer, mockBackend, clients := setUpTestComponents(t)
	defer tearDownTest(t, stsServer, mockBackend)

	federatedTokenReceivedTime := time.Time{}.String()
	accessTokenReceivedTime := time.Time{}.String()
	for i := 0; i < numClient; i++ {
		resp, err := sendHTTPRequestWithRetry(clients[i], genStsReq(t))
		if err != nil {
			t.Fatalf("client %d: failure in sending STS request: %v", i, err)
		}
		verifyStsResponse(t, resp)

		resp, err = sendHTTPRequestWithRetry(clients[i], genDumpReq(t))
		if err != nil {
			t.Fatalf("client %d: failure in sending STS request: %v", i, err)
		}
		federatedTokenReceivedTime, accessTokenReceivedTime =
			verifyDumpResponse(t, resp, federatedTokenReceivedTime, accessTokenReceivedTime)
	}
}

func genDumpReq(t *testing.T) (req *http.Request) {
	dumpURL := "http://" + stsServerAddress + stsServer.StsStatusPath
	req, _ = http.NewRequest("GET", dumpURL, nil)

	reqDump, _ := httputil.DumpRequest(req, true)
	t.Logf("status dump request:\n%s", string(reqDump))
	return req
}

// verifyDumpResponse parses token info from dump response, and verifies that
// issue time of federated token and access token have updated by comparing them
// with oldFTime and oldATime, and returns new issue time of federated token and access token.
func verifyDumpResponse(t *testing.T, resp *http.Response, oldFTime, oldATime string) (newFTime, newATime string) {
	if resp.StatusCode != http.StatusOK {
		t.Errorf("response HTTP status code does not match, get %d vs expected %d",
			resp.StatusCode, http.StatusOK)
	}

	defer resp.Body.Close()
	body, _ := ioutil.ReadAll(resp.Body)
	tokenDump := &stsservice.TokensDump{}
	if err := json.Unmarshal(body, tokenDump); err != nil {
		t.Errorf("failed to unmarshal status dump response: %v", err)
	}
	for _, info := range tokenDump.Tokens {
		if info.TokenType == "access token" {
			newFTime = info.IssueTime
			if newFTime == oldFTime {
				t.Errorf("federated token issue time does not change: %s", newFTime)
			}
		} else {
			newATime = info.IssueTime
			if newATime == oldATime {
				t.Errorf("access token issue time does not change: %s", newATime)
			}
		}
	}
	return newFTime, newATime
}

// verifyStsResponse verifies that received STS response has valid parameter values.
func verifyStsResponse(t *testing.T, resp *http.Response) {
	if resp.StatusCode != http.StatusOK {
		t.Errorf("response HTTP status code does not match, get %d vs expected %d",
			resp.StatusCode, http.StatusOK)
	}
	ctVal := resp.Header.Get("Content-Type")
	if ctVal != "application/json" {
		t.Errorf("response header Content-Type does not match, get %s vs expected application/json",
			ctVal)
	}
	defer resp.Body.Close()
	body, _ := ioutil.ReadAll(resp.Body)
	respStsParam := &stsservice.StsResponseParameters{}
	if err := json.Unmarshal(body, respStsParam); err != nil {
		t.Errorf("failed to unmarshal STS success response: %v", err)
	}
	if respStsParam.AccessToken == "" {
		t.Errorf("failed to get access token from STS response parameters: %+v", respStsParam)
	}
	if respStsParam.IssuedTokenType != "urn:ietf:params:oauth:token-type:access_token" {
		t.Errorf("unexpected issued token type from STS response parameters: %+v", respStsParam)
	}
	if respStsParam.TokenType != "Bearer" {
		t.Errorf("unexpected token type from STS response parameters: %+v", respStsParam)
	}
}

func genStsReq(t *testing.T) (req *http.Request) {
	stsQuery := url.Values{}
	stsQuery.Set("grant_type", stsServer.TokenExchangeGrantType)
	stsQuery.Set("resource", "https//:backend.example.com")
	stsQuery.Set("audience", "audience")
	stsQuery.Set("scope", "https://www.googleapis.com/auth/cloud-platform")
	stsQuery.Set("requested_token_type", "urn:ietf:params:oauth:token-type:access_token")
	stsQuery.Set("subject_token", mock.FakeSubjectToken)
	stsQuery.Set("subject_token_type", stsServer.SubjectTokenType)
	stsQuery.Set("actor_token", "")
	stsQuery.Set("actor_token_type", "")
	stsURL := "http://" + stsServerAddress + stsServer.TokenPath
	req, _ = http.NewRequest("POST", stsURL, strings.NewReader(stsQuery.Encode()))
	req.Header.Set("Content-Type", stsServer.URLEncodedForm)
	reqDump, _ := httputil.DumpRequest(req, true)
	t.Logf("STS request:\n%s", string(reqDump))
	return req
}

func sendHTTPRequestWithRetry(client *http.Client, req *http.Request) (resp *http.Response, err error) {
	for i := 0; i < 10; i++ {
		resp, err = client.Do(req)
		if err == nil {
			return resp, err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return resp, err
}

// setUpTest sets up components for the STS flow, including a STS server, a
// token manager, and an authorization server.
func setUpTestComponents(t *testing.T) (*stsServer.Server, *mock.AuthorizationServer, []*http.Client) {
	// Create mock authorization server
	mockServer, err := mock.StartNewServer(t, mock.Config{Port: 0})
	if err != nil {
		t.Fatalf("failed to start a mock server: %v", err)
	}
	// Create token exchange Google plugin
	tokenExchangePlugin, _ := google.CreateTokenManagerPlugin(mock.FakeTrustDomain, mock.FakeProjectNum, mock.FakeGKEClusterURL, false)
	federatedTokenTestingEndpoint := mockServer.URL + "/v1/identitybindingtoken"
	accessTokenTestingEndpoint := mockServer.URL + "/v1/projects/-/serviceAccounts/service-%s@gcp-sa-meshdataplane.iam.gserviceaccount.com:generateAccessToken"
	tokenExchangePlugin.SetEndpoints(federatedTokenTestingEndpoint, accessTokenTestingEndpoint)
	// Create token manager
	tokenManager := CreateTokenManager(GoogleTokenExchange,
		Config{TrustDomain: mock.FakeTrustDomain})
	tokenManager.(*TokenManager).SetPlugin(tokenExchangePlugin)
	// Create STS server
	portStr := strconv.Itoa(mockServer.Port + 1)
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:"+portStr)
	if err != nil {
		t.Fatalf("failed to create address %v", err)
	}
	server, _ := stsServer.NewServer(stsServer.Config{LocalHostAddr: addr.IP.String(), LocalPort: addr.Port}, tokenManager)
	// Create test client
	stsServerAddress = addr.String()
	clients := []*http.Client{}
	for i := 0; i < numClient; i++ {
		hTTPClient := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					t.Logf("set up server address to dial %s", addr)
					addr = stsServerAddress
					return net.Dial(network, addr)
				},
			},
		}
		clients = append(clients, hTTPClient)
	}
	return server, mockServer, clients
}

func tearDownTest(t *testing.T, stsServer *stsServer.Server, backend *mock.AuthorizationServer) {
	if err := backend.Stop(); err != nil {
		t.Logf("failed to stop mock server: %v", err)
	}
	stsServer.Stop()
}

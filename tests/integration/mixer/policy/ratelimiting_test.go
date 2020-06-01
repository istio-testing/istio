//  Copyright 2018 Istio Authors
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package policy

import (
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"path"
	"strings"
	"testing"

	"istio.io/istio/pkg/test/env"
	"istio.io/istio/pkg/test/framework"
	"istio.io/istio/pkg/test/framework/components/bookinfo"
	"istio.io/istio/pkg/test/framework/components/galley"
	"istio.io/istio/pkg/test/framework/components/ingress"
	"istio.io/istio/pkg/test/framework/components/istio"
	"istio.io/istio/pkg/test/framework/components/mixer"
	"istio.io/istio/pkg/test/framework/components/namespace"
	"istio.io/istio/pkg/test/framework/components/prometheus"
	"istio.io/istio/pkg/test/framework/components/redis"
	"istio.io/istio/pkg/test/framework/label"
	"istio.io/istio/pkg/test/framework/resource"
	"istio.io/istio/pkg/test/framework/resource/environment"
	util "istio.io/istio/tests/integration/mixer"
)

var (
	ist        istio.Instance
	bookinfoNs namespace.Instance
	g          galley.Instance
	red        redis.Instance
	ing        ingress.Instance
	prom       prometheus.Instance
)

func TestRateLimiting_RedisQuotaFixedWindow(t *testing.T) {
	testRedisQuota(t, bookinfo.RatingsRedisRateLimitFixed, "ratings")
}

func TestRateLimiting_RedisQuotaRollingWindow(t *testing.T) {
	testRedisQuota(t, bookinfo.RatingsRedisRateLimitRolling, "ratings")
}

func TestRateLimiting_DefaultLessThanOverride(t *testing.T) {
	framework.
		NewTest(t).
		RequiresEnvironment(environment.Kube).
		Run(func(ctx framework.TestContext) {
			destinationService := "productpage"
			bookInfoNameSpaceStr := bookinfoNs.Name()
			config := setupConfigOrFail(t, bookinfo.ProductPageRedisRateLimit, bookInfoNameSpaceStr,
				red, g, ctx)
			defer deleteConfigOrFail(t, config, g, ctx)
			util.AllowRuleSync(t)

			res := util.SendTraffic(ing, t, "Sending traffic...", "", "", 300)
			got429s := float64(res.RetCodes[http.StatusTooManyRequests])

			if got429s == 0 {
				attributes := []string{fmt.Sprintf("%s=\"%s\"", util.GetDestinationLabel(),
					util.Fqdn(destinationService, bookInfoNameSpaceStr)),
					fmt.Sprintf("%s=\"%d\"", util.GetResponseCodeLabel(), 429),
					fmt.Sprintf("%s=\"%s\"", util.GetReporterCodeLabel(), "destination")}
				t.Logf("prometheus values for istio_requests_total for 429's:\n%s",
					util.PromDumpWithAttributes(prom, "istio_requests_total", attributes))
				t.Errorf("Bad metric value for rate-limited requests (429s): got %f, want more than 1", got429s)
			}
		})
}

func testRedisQuota(t *testing.T, config bookinfo.ConfigFile, destinationService string) {
	framework.NewTest(t).Label(label.Flaky).Run(func(ctx framework.TestContext) {
		g.ApplyConfigOrFail(
			t,
			bookinfoNs,
			bookinfo.NetworkingReviewsV3Rule.LoadWithNamespaceOrFail(t, bookinfoNs.Name()),
		)
		defer g.DeleteConfigOrFail(t,
			bookinfoNs,
			bookinfo.NetworkingReviewsV3Rule.LoadWithNamespaceOrFail(t, bookinfoNs.Name()))
		bookInfoNameSpaceStr := bookinfoNs.Name()
		config := setupConfigOrFail(t, config, bookInfoNameSpaceStr, red, g, ctx)
		defer deleteConfigOrFail(t, config, g, ctx)
		util.AllowRuleSync(t)

		_ = util.SendTraffic(ing, t, "Sending traffic...", "", "", 300)
		_, _ = util.FetchRequestCount(t, prom, destinationService, "",
			bookInfoNameSpaceStr, 300)

		totalReqs := res.DurationHistogram.Count
		succReqs := float64(res.RetCodes[http.StatusOK])
		badReqs := res.RetCodes[http.StatusBadRequest]
		actualDuration := res.ActualDuration.Seconds() // can be a bit more than requested

		t.Log("Successfully sent request(s) to /productpage; checking metrics...")
		t.Logf("Fortio Summary: %d reqs (%f rps, %f 200s (%f rps), %d 400s - %+v)",
			totalReqs, res.ActualQPS, succReqs, succReqs/actualDuration, badReqs, res.RetCodes)

		got429s, _ := util.FetchRequestCount(t, prom, destinationService, "", bookInfoNameSpaceStr,
			300)
		if got429s == 0 {
			attributes := []string{fmt.Sprintf("%s=\"%s\"", util.GetDestinationLabel(),
				util.Fqdn(destinationService, bookInfoNameSpaceStr)),
				fmt.Sprintf("%s=\"%d\"", util.GetResponseCodeLabel(), 429),
				fmt.Sprintf("%s=\"%s\"", util.GetReporterCodeLabel(), "destination")}
			t.Logf("prometheus values for istio_requests_total for 429's:\n%s",
				util.PromDumpWithAttributes(prom, "istio_requests_total", attributes))
			t.Errorf("Could not find 429s")
		}

	})
}

func setupConfigOrFail(t *testing.T, config bookinfo.ConfigFile, bookInfoNameSpaceStr string,
	red redis.Instance, g galley.Instance, ctx resource.Context) string {
	p := path.Join(env.BookInfoRoot, string(config))
	content, err := ioutil.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	con := string(content)

	con = strings.Replace(con, "redisServerUrl: redis-release-master:6379",
		"redisServerUrl: redis-release-master."+red.GetRedisNamespace()+":6379", -1)
	con = strings.Replace(con, "namespace: default",
		"namespace: "+bookInfoNameSpaceStr, -1)

	ns := namespace.ClaimOrFail(t, ctx, ist.Settings().SystemNamespace)
	g.ApplyConfigOrFail(t, ns, con)
	return con
}

func deleteConfigOrFail(t *testing.T, config string, g galley.Instance, ctx resource.Context) {
	ns := namespace.ClaimOrFail(t, ctx, ist.Settings().SystemNamespace)
	g.DeleteConfigOrFail(t, ns, config)
}

func TestMain(m *testing.M) {
	framework.
		NewSuite("mixer_policy_ratelimit", m).
		Label(label.CustomSetup).
		RequireEnvironment(environment.Kube).
		RequireSingleCluster().
		SetupOnEnv(environment.Kube, istio.Setup(&ist, func(cfg *istio.Config) {
			cfg.ControlPlaneValues = `
values:
  prometheus:
    enabled: true
  global:
    disablePolicyChecks: false
  telemetry:
    v1:
      enabled: true
    v2:
      enabled: false
components:
  policy:
    enabled: true
  telemetry:
    enabled: true`
		})).
		Setup(testsetup).
		Run()
}

func testsetup(ctx resource.Context) (err error) {
	bookinfoNs, err = namespace.New(ctx, namespace.Config{
		Prefix: "istio-bookinfo",
		Inject: true,
	})
	if err != nil {
		return
	}
	if _, err = bookinfo.Deploy(ctx, bookinfo.Config{Namespace: bookinfoNs, Cfg: bookinfo.BookInfo}); err != nil {
		return
	}
	g, err = galley.New(ctx, galley.Config{})
	if err != nil {
		return
	}
	if _, err = mixer.New(ctx, mixer.Config{Galley: g}); err != nil {
		return
	}
	red, err = redis.New(ctx, redis.Config{})
	if err != nil {
		return
	}
	ing, err = ingress.New(ctx, ingress.Config{Istio: ist})
	if err != nil {
		return
	}
	prom, err = prometheus.New(ctx, prometheus.Config{})
	if err != nil {
		return
	}

	bookinfoGatewayFile, err := bookinfo.NetworkingBookinfoGateway.LoadGatewayFileWithNamespace(bookinfoNs.Name())
	if err != nil {
		return
	}
	destinationRule, err := bookinfo.GetDestinationRuleConfigFile(ctx)
	if err != nil {
		return
	}
	destinationRuleFile, err := destinationRule.LoadWithNamespace(bookinfoNs.Name())
	if err != nil {
		return
	}
	virtualServiceFile, err := bookinfo.NetworkingVirtualServiceAllV1.LoadWithNamespace(bookinfoNs.Name())
	if err != nil {
		return
	}
	err = g.ApplyConfig(bookinfoNs,
		bookinfoGatewayFile,
		destinationRuleFile,
		virtualServiceFile)
	if err != nil {
		return
	}

	return nil
}

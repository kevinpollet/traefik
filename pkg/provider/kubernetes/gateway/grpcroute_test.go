package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
	gatev1 "sigs.k8s.io/gateway-api/apis/v1"
)

func Test_buildGRPCMatchRule(t *testing.T) {
	testCases := []struct {
		desc             string
		routeMatch       gatev1.GRPCRouteMatch
		hostnames        []gatev1.Hostname
		expectedRule     string
		expectedPriority int
		expectedError    bool
	}{
		{
			desc:             "Empty rule and matches ",
			expectedRule:     "PathPrefix(`/`)",
			expectedPriority: 15,
		},
		{
			desc:             "One Host rule without match",
			hostnames:        []gatev1.Hostname{"foo.com"},
			expectedRule:     "Host(`foo.com`) && PathPrefix(`/`)",
			expectedPriority: 22,
		},
		{
			desc: "One GRPCRouteMatch with nil values",
			routeMatch: gatev1.GRPCRouteMatch{
				Method: ptr.To(gatev1.GRPCMethodMatch{
					Type:    nil,
					Service: nil,
					Method:  nil,
				}),
				Headers: nil,
			},
			expectedRule:     "PathPrefix(`/`)",
			expectedPriority: 15,
		},
		{
			desc:      "One GRPCRouteMatch with nil values and hostname",
			hostnames: []gatev1.Hostname{"foo.com"},
			routeMatch: gatev1.GRPCRouteMatch{
				Method: ptr.To(gatev1.GRPCMethodMatch{
					Type:    nil,
					Service: nil,
					Method:  nil,
				}),
				Headers: nil,
			},
			expectedRule:     "Host(`foo.com`) && PathPrefix(`/`)",
			expectedPriority: 22,
		},
		{
			desc: "One GRPCRouteMatch with only service",
			routeMatch: gatev1.GRPCRouteMatch{
				Method: ptr.To(gatev1.GRPCMethodMatch{
					Service: ptr.To("foobar"),
				}),
				Headers: nil,
			},
			expectedRule:     "PathRegexp(`/foobar/[^/]+`)",
			expectedPriority: 27,
		},
		{
			desc: "One GRPCRouteMatch with only service and Exact type match",
			routeMatch: gatev1.GRPCRouteMatch{
				Method: ptr.To(gatev1.GRPCMethodMatch{
					Type:    ptr.To(gatev1.GRPCMethodMatchExact),
					Service: ptr.To("foobar"),
				}),
				Headers: nil,
			},
			expectedRule:     "PathRegexp(`/foobar/[^/]+`)",
			expectedPriority: 27,
		},
		{
			desc: "One GRPCRouteMatch with only service and Regex type match",
			routeMatch: gatev1.GRPCRouteMatch{
				Method: ptr.To(gatev1.GRPCMethodMatch{
					Type:    ptr.To(gatev1.GRPCMethodMatchRegularExpression),
					Service: ptr.To("foobar"),
				}),
				Headers: nil,
			},
			expectedRule:     "PathRegexp(`/foobar/[^/]+`)",
			expectedPriority: 27,
		},
		{
			desc:      "One GRPCRouteMatch with only service and hostname",
			hostnames: []gatev1.Hostname{"foo.com"},
			routeMatch: gatev1.GRPCRouteMatch{
				Method: ptr.To(gatev1.GRPCMethodMatch{
					Service: ptr.To("foobar"),
				}),
				Headers: nil,
			},
			expectedRule:     "Host(`foo.com`) && PathRegexp(`/foobar/[^/]+`)",
			expectedPriority: 34,
		},
		{
			desc: "One GRPCRouteMatch with only method",
			routeMatch: gatev1.GRPCRouteMatch{
				Method: ptr.To(gatev1.GRPCMethodMatch{
					Method: ptr.To("foobar"),
				}),
				Headers: nil,
			},
			expectedRule:     "PathRegexp(`/[^/]+/foobar`)",
			expectedPriority: 27,
		},
		{
			desc:      "One GRPCRouteMatch with only method and hostname",
			hostnames: []gatev1.Hostname{"foo.com"},
			routeMatch: gatev1.GRPCRouteMatch{
				Method: ptr.To(gatev1.GRPCMethodMatch{
					Type:    nil,
					Service: nil,
					Method:  ptr.To("foobar"),
				}),
				Headers: nil,
			},
			expectedRule:     "Host(`foo.com`) && PathRegexp(`/[^/]+/foobar`)",
			expectedPriority: 34,
		},
		{
			desc: "One GRPCRouteMatch with service and method",
			routeMatch: gatev1.GRPCRouteMatch{
				Method: ptr.To(gatev1.GRPCMethodMatch{
					Type:    nil,
					Service: ptr.To("foobar"),
					Method:  ptr.To("foobar"),
				}),
				Headers: nil,
			},
			expectedRule:     "PathRegexp(`/foobar/foobar`)",
			expectedPriority: 28,
		},
		{
			desc:      "One GRPCRouteMatch with service and method and hostname",
			hostnames: []gatev1.Hostname{"foo.com"},
			routeMatch: gatev1.GRPCRouteMatch{
				Method: ptr.To(gatev1.GRPCMethodMatch{
					Type:    nil,
					Service: ptr.To("foobar"),
					Method:  ptr.To("foobar"),
				}),
				Headers: nil,
			},
			expectedRule:     "Host(`foo.com`) && PathRegexp(`/foobar/foobar`)",
			expectedPriority: 35,
		},
		{
			desc: "One GRPCRouteMatch with one header",
			routeMatch: gatev1.GRPCRouteMatch{
				Headers: []gatev1.GRPCHeaderMatch{{
					Name:  "foo",
					Value: "bar",
				}},
			},
			expectedRule:     "PathPrefix(`/`) && Header(`foo`,`bar`)",
			expectedPriority: 38,
		},
		{
			desc: "One GRPCRouteMatch with one header with exact match",
			routeMatch: gatev1.GRPCRouteMatch{
				Headers: []gatev1.GRPCHeaderMatch{{
					Type:  ptr.To(gatev1.HeaderMatchExact),
					Name:  "foo",
					Value: "bar",
				}},
			},
			expectedRule:     "PathPrefix(`/`) && Header(`foo`,`bar`)",
			expectedPriority: 38,
		},
		{
			desc: "One GRPCRouteMatch with one header with regex match",
			routeMatch: gatev1.GRPCRouteMatch{
				Headers: []gatev1.GRPCHeaderMatch{{
					Type:  ptr.To(gatev1.HeaderMatchRegularExpression),
					Name:  "foo",
					Value: "bar",
				}},
			},
			expectedRule:     "PathPrefix(`/`) && HeaderRegexp(`foo`,`bar`)",
			expectedPriority: 44,
		},
		{
			desc:      "One GRPCRouteMatch with one header match and hostname",
			hostnames: []gatev1.Hostname{"foo.com"},
			routeMatch: gatev1.GRPCRouteMatch{
				Headers: []gatev1.GRPCHeaderMatch{{
					Name:  "foo",
					Value: "bar",
				}},
			},
			expectedRule:     "Host(`foo.com`) && PathPrefix(`/`) && Header(`foo`,`bar`)",
			expectedPriority: 45,
		},
		{
			desc: "One GRPCRouteMatch with multiple header",
			routeMatch: gatev1.GRPCRouteMatch{
				Headers: []gatev1.GRPCHeaderMatch{
					{
						Name:  "foo",
						Value: "bar",
					},
					{
						Name:  "foo2",
						Value: "bar2",
					},
				},
			},
			expectedRule:     "PathPrefix(`/`) && Header(`foo`,`bar`) && Header(`foo2`,`bar2`)",
			expectedPriority: 63,
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()

			rule, priority := buildGRPCMatchRule(test.hostnames, test.routeMatch)
			assert.Equal(t, test.expectedRule, rule)
			assert.Equal(t, test.expectedPriority, priority)
		})
	}
}

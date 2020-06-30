package dynamic

import (
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/containous/traefik/v2/pkg/config/file"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

func TestDeepCopy(t *testing.T) {
	cfg := &Configuration{}
	_, err := toml.DecodeFile("./fixtures/sample.toml", &cfg)
	require.NoError(t, err)

	cfgCopy := cfg
	assert.Equal(t, reflect.ValueOf(cfgCopy), reflect.ValueOf(cfg))
	assert.Equal(t, reflect.ValueOf(cfgCopy), reflect.ValueOf(cfg))
	assert.Equal(t, cfgCopy, cfg)

	cfgDeepCopy := cfg.DeepCopy()
	assert.NotEqual(t, reflect.ValueOf(cfgDeepCopy), reflect.ValueOf(cfg))
	assert.Equal(t, reflect.TypeOf(cfgDeepCopy), reflect.TypeOf(cfg))
	assert.Equal(t, cfgDeepCopy, cfg)

	// Update cfg
	cfg.HTTP.Routers["powpow"] = &Router{}

	assert.Equal(t, reflect.ValueOf(cfgCopy), reflect.ValueOf(cfg))
	assert.Equal(t, reflect.ValueOf(cfgCopy), reflect.ValueOf(cfg))
	assert.Equal(t, cfgCopy, cfg)

	assert.NotEqual(t, reflect.ValueOf(cfgDeepCopy), reflect.ValueOf(cfg))
	assert.Equal(t, reflect.TypeOf(cfgDeepCopy), reflect.TypeOf(cfg))
	assert.NotEqual(t, cfgDeepCopy, cfg)
}

func TestDecodeContentFromMarshalledConfig(t *testing.T) {
	marshalledConfig := &Configuration{
		HTTP: &HTTPConfiguration{
			Services: map[string]*Service{
				"service": {
					LoadBalancer: &ServersLoadBalancer{
						Servers: []Server{
							{
								URL: "http://foo:80",
							},
						},
					},
				},
			},
		},
	}

	configData, err := yaml.Marshal(marshalledConfig)
	require.NoError(t, err)

	unmarshalledConfig := &Configuration{}

	err = file.DecodeContent(string(configData), ".yaml", unmarshalledConfig)
	require.NoError(t, err)
}

package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/naoina/toml"
	"github.com/withmandala/go-log"
)

type tomlConfigHTTP struct {
	Port int
}

type tomlConfigMQTT struct {
	BrokerHost     string
	BrokerPort     int
	BrokerUsername string
	BrokerPassword string
	ClientId       string
	TopicPrefix    string
	Topic          string
}

type tomlConfig struct {
	Http tomlConfigHTTP
	Mqtt tomlConfigMQTT
}

// set up a global logger...
// see: https://stackoverflow.com/a/43827612/57626
var logger *log.Logger
var config tomlConfig
var client mqtt.Client

var connectHandler mqtt.OnConnectHandler = func(client mqtt.Client) {
	r := client.OptionsReader()
	logger.Infof("connected to MQTT at %s", r.Servers())
}

var connectLostHandler mqtt.ConnectionLostHandler = func(client mqtt.Client, err error) {
	logger.Errorf("Connect lost: %v", err)
}

func main() {
	logger = log.New(os.Stderr).WithColor()

	configFile := flag.String("config", "", "Filename with configuration")
	flag.Parse()

	if *configFile != "" {
		f, err := os.Open(*configFile)
		if err != nil {
			panic(err)
		}
		defer f.Close()
		if err := toml.NewDecoder(f).Decode(&config); err != nil {
			panic(err)
		}
	} else {
		logger.Fatal("Must specify configuration file with -config FILENAME")
	}

	opts := mqtt.NewClientOptions()

	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", config.Mqtt.BrokerHost, config.Mqtt.BrokerPort))
	if config.Mqtt.BrokerPassword != "" && config.Mqtt.BrokerUsername != "" {
		opts.SetUsername(config.Mqtt.BrokerUsername)
		opts.SetPassword(config.Mqtt.BrokerPassword)
	}
	opts.SetClientID(config.Mqtt.ClientId)
	opts.OnConnect = connectHandler
	opts.OnConnectionLost = connectLostHandler

	client = mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	}

	http.HandleFunc("/", processData)
	http.HandleFunc("/data/report/", processData)

	//Use the default DefaultServeMux.
	var port string = fmt.Sprintf(":%d", config.Http.Port)
	logger.Infof("listening for inbound Ambient Weather HTTP requests on %s", port)
	err := http.ListenAndServe(port, nil)
	if err != nil {
		logger.Fatal(err)
	}
}

func processData(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	logger.Infof("Request: host=%s, user-agent=%s url=%s", r.RemoteAddr, r.UserAgent(), r.URL)
	for key, val := range query {
		logger.Infof("%s = %s", key, val[0])
		topic := fmt.Sprintf("%s/%s/%s", config.Mqtt.TopicPrefix, config.Mqtt.Topic, key)
		// args are: topic, qos, retain, value
		token := client.Publish(topic, 0, false, val[0])
		token.Wait()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	retval := fmt.Sprintf("{ \"status\": \"accepted\", \"num_values\": %d }", len(query))
	w.Write([]byte(retval))
}

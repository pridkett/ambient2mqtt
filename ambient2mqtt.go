package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "embed"
	"encoding/json"
	"reflect"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/naoina/toml"
	"github.com/withmandala/go-log"

	_ "github.com/influxdata/influxdb1-client" // this is important because of the bug in go mod
	influxclient "github.com/influxdata/influxdb1-client/v2"
)

type hassMqttConfigDevice struct {
	Identifiers  []string `json:"identifiers"`
	Manufacturer string   `json:"manufacturer"`
	Model        string   `json:"model"`
	Name         string   `json:"name"`
	SWVersion    string   `json:"sw_version"`
}

type hassMqttConfig struct {
	AvailabilityTopic string               `json:"availability_topic"`
	ConfigTopic       string               `json:"-"`
	Device            hassMqttConfigDevice `json:"device"`
	Name              string               `json:"name"`
	Qos               int                  `json:"qos"`
	StateTopic        string               `json:"state_topic"`
	UniqueId          string               `json:"unique_id"`
	Icon              string               `json:"icon,omitempty"`
	UnitOfMeasurement string               `json:"unit_of_measurement"`
	Platform          string               `json:"-"`
}

// HomeAssistant specific settings overall configuration
type tomlConfigHass struct {
	Discovery       bool
	DiscoveryPrefix string
	ObjectId        string
	DeviceModel     string
	DeviceName      string
	Manufacturer    string
}

// HTTP settings for overall configuration
type tomlConfigHTTP struct {
	Port int
}

// MQTT settings for overall configuration
type tomlConfigMQTT struct {
	BrokerHost     string
	BrokerPort     int
	BrokerUsername string
	BrokerPassword string
	ClientId       string
	TopicPrefix    string
	Topic          string
}

type tomlConfigInflux struct {
	Hostname string
	Port     int
	Database string
}

// Master configuration structure
// This is usually passed in on the command line
type tomlConfig struct {
	Http   tomlConfigHTTP
	Mqtt   tomlConfigMQTT
	Hass   tomlConfigHass
	Influx tomlConfigInflux
}

// a single component definition needed for HomeAssistant
// these are used to create the auto configuration blocks
// needed for Home Assistant.
type tomlHassComponent struct {
	Platform    *string
	DeviceClass *string
	Icon        *string
	UnitClass   *string
	Unit        *string
	Name        *string
}

// the complete collection of known components
// these are expected to be embedded into the program during installation time
type tomlComponents struct {
	Sensors map[string]tomlHassComponent
}

// set up a global logger...
// see: https://stackoverflow.com/a/43827612/57626
var logger *log.Logger

var config tomlConfig
var components tomlComponents
var client mqtt.Client

//go:embed components.toml
var components_string []byte

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

	if err := toml.Unmarshal(components_string, &components); err != nil {
		panic(err)
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
	http.HandleFunc("/data/report", processData)
	http.HandleFunc("/data/report/", processData)

	//Use the default DefaultServeMux.
	var port string = fmt.Sprintf(":%d", config.Http.Port)
	logger.Infof("listening for inbound Ambient Weather HTTP requests on %s", port)
	err := http.ListenAndServe(port, nil)
	if err != nil {
		logger.Fatal(err)
	}
}

func getHassMQTTAvailabilityTopic(sensor_type string, unique_id string, key string) string {
	return fmt.Sprintf("homeassistant/%s/%s/%s/availability", sensor_type, unique_id, key)
}

func getHassMQTTStateTopic(sensor_type string, unique_id string, key string) string {
	return fmt.Sprintf("homeassistant/%s/%s/%s/state", sensor_type, unique_id, key)
}

func getHassMQTTConfigTopic(sensor_type string, unique_id string, key string) string {
	return fmt.Sprintf("homeassistant/%s/%s/%s/config", sensor_type, unique_id, key)
}

func getHassMQTTUniqueId(key string, unique_id string) string {
	return fmt.Sprintf("%s_%s", unique_id, key)
}

func getHassMQTTConfig(key string, unique_id string) hassMqttConfig {
	device_config := hassMqttConfig{}
	device := hassMqttConfigDevice{}
	device.Identifiers = append(device.Identifiers, unique_id)

	device.Model = config.Hass.DeviceModel
	if device.Model == "" {
		device.Model = "ws-2902a"
	}

	device.Name = config.Hass.DeviceName
	if device.Name == "" {
		device.Name = "ws-2902a"
	}

	device.Manufacturer = config.Hass.Manufacturer
	if device.Manufacturer == "" {
		device.Manufacturer = "Ambient Weather"
	}

	if value, ok := components.Sensors[key]; ok {
		if value.Name != nil {
			device_config.Name = *value.Name
		} else {
			device_config.Name = key
		}
		// logger.Infof("Device Name: %s", device_config.Name)
		device_config.Platform = *value.Platform
		if value.Unit != nil {
			device_config.UnitOfMeasurement = *value.Unit
		}
		device_config.AvailabilityTopic = getHassMQTTAvailabilityTopic(*value.Platform, unique_id, device_config.Name)
		device_config.StateTopic = getHassMQTTStateTopic(*value.Platform, unique_id, device_config.Name)
		device_config.ConfigTopic = getHassMQTTConfigTopic(*value.Platform, unique_id, device_config.Name)
		device_config.Device = device
		device_config.Qos = 1
		device_config.UniqueId = getHassMQTTUniqueId(key, unique_id)
	}

	return device_config
}

func packComponentConfig(component hassMqttConfig) []byte {
	res, err := json.Marshal(component)
	if err != nil {
		logger.Fatalf("unable to marshal JSON: %s", err)
	}
	return res
}

// see: https://www.golangprograms.com/go-language/arrays.htmlç
func arrayContains(arrayType interface{}, item interface{}) bool {
	arr := reflect.ValueOf(arrayType)

	if arr.Kind() != reflect.Array && arr.Kind() != reflect.Slice {
		logger.Fatalf("Data Type: %s", arr.Kind())
		panic("Invalid data-type")
	}

	for i := 0; i < arr.Len(); i++ {
		if arr.Index(i).Interface() == item {
			return true
		}
	}

	return false
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

	// HomeAssistant has a very specific way it wants things to appear on the MQTT bus
	if config.Hass.Discovery {
		var objectId string
		ignoredFields := []string{"PASSKEY", "stationtype", "dateutc"}

		if config.Hass.ObjectId != "" {
			objectId = config.Hass.ObjectId
		} else {
			objectId = strings.Replace(query.Get("PASSKEY"), ":", "-", -1)
		}

		var stationType string
		if query.Get("stationtype") != "" {
			stationType = string(query.Get("stationtype"))
		}

		for key, value := range query {
			if arrayContains(ignoredFields, key) {
				continue
			}

			component := getHassMQTTConfig(key, objectId)
			component.Device.SWVersion = stationType

			if component.Platform != "" {
				logger.Infof("processed key %s - topic %s", key, component.AvailabilityTopic)
				token := client.Publish(component.AvailabilityTopic, byte(component.Qos), false, "online")
				token.Wait()
				token = client.Publish(component.StateTopic, byte(component.Qos), false, value[0])
				token.Wait()
				token = client.Publish(component.ConfigTopic, byte(component.Qos), false, packComponentConfig(component))
				token.Wait()
				// topic := fmt.Sprintf("%s/%s/%s/config", config.Hass.DiscoveryPrefix, *component.platform, objectId)
				// token := client.Publish(topic, 0, false, val[0])
				// token.Wait()
			} else {
				logger.Warnf("got a key of %s - I don't know what to do with this", key)
			}
		}
	}

	write_influx(query)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	retval := fmt.Sprintf("{ \"status\": \"accepted\", \"num_values\": %d }", len(query))
	w.Write([]byte(retval))
}

func simple_parseFloat(s string) float64 {
	rv, err := strconv.ParseFloat(s, 64)
	if err != nil {
		logger.Fatalf("error converting \"%s\" to float64: %s", s, err)
	}
	return rv
}

func simple_parseInt(s string) int64 {
	rv, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		logger.Fatalf("error converting \"%s\" to int32: %s", s, err)
	}
	return rv
}

func write_influx(query url.Values) {
	ignoredFields := []string{"PASSKEY", "stationtype", "dateutc"}

	// 	tempf=63.1 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: eventrainin=0.000 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: dailyrainin=0.000 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: solarradiation=645.94 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: uv=6 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: baromabsin=29.534 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: maxdailygust=11.4 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: batt_co2=1 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: humidityin=36 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: baromrelin=29.953 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: humidity=35 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: winddir=292 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: windgustmph=2.2 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: hourlyrainin=0.000 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: weeklyrainin=0.000 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: tempinf=64.0 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: windspeedmph=2.2 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: monthlyrainin=0.000 (type=string)
	// [INFO]  2022/05/01 17:20:36 Adding Key: totalrainin=15.610 (type=string)

	intfields := []string{"uv", "batt_co2", "winddir", "humidity", "humidityin"}

	c, err := influxclient.NewHTTPClient(influxclient.HTTPConfig{
		Addr: fmt.Sprintf("http://%s:%d", config.Influx.Hostname, config.Influx.Port),
	})
	if err != nil {
		logger.Errorf("Error creating InfluxDB Client: ", err.Error())
	}
	defer c.Close()

	bp, err := influxclient.NewBatchPoints(influxclient.BatchPointsConfig{
		Database:  config.Influx.Database,
		Precision: "s",
	})

	if err != nil {
		logger.Errorf("error creating batchpoints: %s", err)
	}

	tags := map[string]string{"host": "edgewater"}
	values := map[string]interface{}{}

	for key, value := range query {
		if arrayContains(ignoredFields, key) {
			continue
		}
		if arrayContains(intfields, key) {
			values[key] = simple_parseInt(value[0])
		} else {
			values[key] = simple_parseFloat(value[0])
		}
		logger.Infof("Added Key: %s=%v (type=%s)", key, values[key], reflect.TypeOf(values[key]))
	}

	point, err := influxclient.NewPoint("weather", tags, values, time.Now())
	if err != nil {
		logger.Fatalf("Error: %s", err)
	}

	bp.AddPoint(point)

	err = c.Write(bp)

	if err != nil {
		logger.Fatal(err)
	}
}

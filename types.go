package main

type Versions map[string]Answers
type Answers map[string]interface{}

type Interim struct {
	UUIDToService                   map[string]map[string]interface{}
	UUIDToContainer                 map[string]map[string]interface{}
	UUIDToStack                     map[string]map[string]interface{}
	UUIDToHost                      map[string]map[string]interface{}
	ServiceUUIDNameToContainersUUID map[string][]string
	StackUUIDToServicesUUID         map[string][]string
	ContainerUUIDToContainerLink    map[string]map[string]interface{}
	ServiceUUIDToServiceLink        map[string]map[string]interface{}

	Networks    []interface{}
	Default     map[string]interface{}
	Environment map[string]interface{}
	Credentials []Credential
}

type Credential struct {
	URL         string
	PublicValue string
	SecretValue string
}

type MetadataDelta struct {
	Version string `json:"Version"`
	Data    []byte `json:"Data"`
}

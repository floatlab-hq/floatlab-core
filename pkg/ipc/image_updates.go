package ipc

type ImageLookupPayload struct {
	Image   string   `json:"image"`
	Policy  string   `json:"policy,omitempty"`
	Digests []string `json:"digests,omitempty"`
}

type ImageLookupResult struct {
	Image   string `json:"image,omitempty"`
	Warning string `json:"warning,omitempty"`
}

type RunningImage struct {
	Reference string   `json:"reference"`
	Digests   []string `json:"digests,omitempty"`
}

type ComposeRunningImages struct {
	Matches bool                    `json:"matches"`
	Images  map[string]RunningImage `json:"images"`
}

type InspectStackImagesPayload struct {
	ComposeUpPayload
	Source string `json:"source"`
}

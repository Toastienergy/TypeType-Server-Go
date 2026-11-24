package portability

// NewRegistry registers the adapters written so far; settings normalizes a TypeType settings section.
func NewRegistry(settings func([]byte) ([]byte, error)) *Registry {
	return &Registry{adapters: []Adapter{
		typeTypeAdapter{settings: func(raw []byte) ([]byte, error) { return settings(raw) }},
		newPipeAdapter{pipePipeDB},
		newPipeAdapter{newPipeDB},
		invidiousAdapter{},
		pipedAdapter{},
		libreTubeAdapter{},
		viewTubeAdapter{},
		flowAdapter{},
		grayjayAdapter{},
		opmlAdapter{OPML},
		materialiousAdapter{},
		opmlAdapter{SkyTube},
		opmlAdapter{YoutubeLocal},
	}}
}

// ParseFormat reads the optional ?format= hint.
func ParseFormat(v string, given bool) (Format, error) {
	if !given {
		return "", nil
	}
	for _, f := range Formats {
		if string(f) == v {
			return f, nil
		}
	}
	return "", invalid("Unsupported portability format")
}

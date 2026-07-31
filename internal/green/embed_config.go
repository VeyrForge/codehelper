package green

// DefaultEmbedOnlyConfig returns a green.json profile that runs only the tiny
// multilingual embed server (Granite ~97M / ~195 MB on first use). It does not
// configure a chat/LLM enrich server — that avoids multi-GB downloads when the
// user only wants semantic rerank.
//
// geCmd is the ge binary name or absolute path (default "ge").
func DefaultEmbedOnlyConfig(geCmd string) Config {
	if geCmd == "" {
		geCmd = "ge"
	}
	return Config{
		Enabled: true,
		Servers: []Server{
			{
				Name:         "embed",
				Cmd:          geCmd,
				Args:         []string{"embed", "serve", "--mcp", "--port", "{{port}}"},
				Port:         8766,
				HealthPath:   "/v1/models",
				URLEnv:       "CODEHELPER_EMBED_URL",
				Env:          map[string]string{"CODEHELPER_EMBED_MODEL": "granite-embedding"},
				StartTimeout: 180, // first serve may download the ~195 MB model
			},
		},
	}
}

// DefaultFullConfig returns embed + enrich (chat) servers. Enrich may pull a
// multi-GB GGUF on first start — prefer DefaultEmbedOnlyConfig unless the user
// explicitly wants index-time enrichment.
func DefaultFullConfig(geCmd string) Config {
	cfg := DefaultEmbedOnlyConfig(geCmd)
	cfg.Servers = append(cfg.Servers, Server{
		Name:         "llm",
		Cmd:          cfg.Servers[0].Cmd,
		Args:         []string{"chat", "serve", "--mcp", "--port", "{{port}}"},
		Port:         8767,
		HealthPath:   "/v1/models",
		URLEnv:       "CODEHELPER_ENRICH_URL",
		Env:          map[string]string{"CODEHELPER_ENRICH_MODEL": "Llama-3.2-1B-Instruct"},
		StartTimeout: 300,
	})
	return cfg
}

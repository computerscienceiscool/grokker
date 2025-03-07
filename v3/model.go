package grokker

import (
	"fmt"

	oai "github.com/sashabaranov/go-openai"
	. "github.com/stevegt/goadapt"
)

var DefaultModel = "gpt-4"

// Model is a type for model name and characteristics
type Model struct {
	Name       string
	TokenLimit int
	oaiModel   string
	active     bool
}

func (m *Model) String() string {
	status := ""
	if m.active {
		status = "*"
	}
	return fmt.Sprintf("%1s %-20s tokens: %d)", status, m.Name, m.TokenLimit)
}

// getModel returns the current model name and model_t from the db
func (g *GrokkerInternal) getModel() (model string, m *Model, err error) {
	defer Return(&err)
	model, m, err = g.models.findModel(g.Model)
	Ck(err)
	return
}

// Models is a type that manages the set of available models.
type Models struct {
	// The list of available models.
	Available map[string]*Model
}

// newModels creates a new Models object.
func newModels() (m *Models) {
	m = &Models{}
	m.Available = map[string]*Model{
		"gpt-3.5-turbo":       {"", 4096, oai.GPT3Dot5Turbo, false},
		"gpt-4":               {"", 8192, oai.GPT4, false},
		"gpt-4-32k":           {"", 32768, oai.GPT432K, false},
		"gpt-4-turbo-preview": {"", 128000, oai.GPT4TurboPreview, false},
	}
	// fill in the model names
	for k, v := range m.Available {
		v.Name = k
		m.Available[k] = v
	}
	return
}

// findModel returns the model name and model_t given a model name.
// if the given model name is empty, then use DefaultModel.
func (models *Models) findModel(model string) (name string, m *Model, err error) {
	if model == "" {
		model = DefaultModel
	}
	m, ok := models.Available[model]
	if !ok {
		err = fmt.Errorf("model %q not found", model)
		return
	}
	name = model
	return
}

// setup the model and oai clients.
// This function needs to be idempotent because it might be called multiple
// times during the lifetime of a Grokker object.
func (g *GrokkerInternal) setup(model string) (err error) {
	defer Return(&err)
	err = g.initModel(model)
	Ck(err)
	g.initClients()
	return
}

// initModel initializes the model for a new or reloaded Grokker database.
// This function needs to be idempotent because it might be called multiple
// times during the lifetime of a Grokker object.
func (g *GrokkerInternal) initModel(model string) (err error) {
	defer Return(&err)
	Assert(g.Root != "", "root directory not set")
	g.models = newModels()
	model, m, err := g.models.findModel(model)
	Ck(err)
	m.active = true
	g.Model = model
	g.oaiModel = m.oaiModel
	// XXX replace with a real tokenizer.
	// charsPerToken := 3.1
	// g.maxChunkLen = int(math.Floor(float64(m.TokenLimit) * charsPerToken))
	// XXX replace with a real tokenizer.
	// g.maxEmbeddingChunkLen = int(math.Floor(float64(8192) * charsPerToken))
	g.tokenLimit = m.TokenLimit
	// XXX 8192 hardcoded for the text-embedding-ada-002 model
	g.embeddingTokenLimit = 8192
	return
}

package grokker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/stevegt/goadapt"

	"github.com/fabiustech/openai"
	fabius_models "github.com/fabiustech/openai/models"

	oai "github.com/sashabaranov/go-openai"
)

// Grokker is a library for analyzing a set of documents and asking
// questions about them using the OpenAI chat and embeddings APIs.
//
// It uses this algorithm (generated by ChatGPT):
//
// To use embeddings in conjunction with the OpenAI Chat API to
// analyze a document, you can follow these general steps:
//
// (1) Break up the document into smaller text chunks or passages,
// each with a length of up to 8192 tokens (the maximum input size for
// the text-embedding-ada-002 model used by the Embeddings API).
//
// (2) For each text chunk, generate an embedding using the
// openai.Embedding.create() function. Store the embeddings for each
// chunk in a data structure such as a list or dictionary.
//
// (3) Use the Chat API to ask questions about the document. To do
// this, you can use the openai.Completion.create() function,
// providing the text of the previous conversation as the prompt
// parameter.
//
// (4) When a question is asked, use the embeddings of the document
// chunks to find the most relevant passages for the question. You can
// use a similarity measure such as cosine similarity to compare the
// embeddings of the question and each document chunk, and return the
// chunks with the highest similarity scores.
//
// (5) Provide the most relevant document chunks to the
// openai.Completion.create() function as additional context for
// generating a response. This will allow the model to better
// understand the context of the question and generate a more relevant
// response.
//
// Repeat steps 3-5 for each question asked, updating the conversation
// prompt as needed.

const (
	version = "1.0.0"
)

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

// Models is a type that manages the set of available models.
type Models struct {
	// The list of available models.
	Available map[string]*Model
}

// NewModels creates a new Models object.
func NewModels() (m *Models) {
	m = &Models{}
	m.Available = map[string]*Model{
		"gpt-3.5-turbo": {"", 4096, oai.GPT3Dot5Turbo, false},
		"gpt-4":         {"", 8192, oai.GPT4, false},
		"gpt-4-32k":     {"", 32768, oai.GPT432K, false}, // XXX deprecated in openai-go 1.9.0
		// "gpt-4-32k": {"", 32768, oai.GPT4_32K, false}, // XXX future version of openai-go
	}
	// fill in the model names
	for k, v := range m.Available {
		v.Name = k
		m.Available[k] = v
	}
	return
}

// ls returns a list of available models.
func (models *Models) ls() (list []*Model) {
	for _, v := range models.Available {
		list = append(list, v)
	}
	return
}

var DefaultModel = "gpt-3.5-turbo"

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

// Document is a single document in a document repository.
type Document struct {
	// XXX deprecated because we weren't precise about what it meant.
	Path string
	// The path to the document file, relative to g.Root
	RelPath string
}

// AbsPath returns the absolute path of a document.
func (g *Grokker) AbsPath(doc *Document) string {
	return filepath.Join(g.Root, doc.RelPath)
}

// Chunk is a single chunk of text from a document.
type Chunk struct {
	// The document that this chunk is from.
	// XXX this is redundant; we could just use the document's path.
	// XXX a chunk should be able to be from multiple documents.
	Document *Document
	// The text of the chunk.
	Text string
	// The embedding of the chunk.
	Embedding []float64
}

type Grokker struct {
	embeddingClient *openai.Client
	chatClient      *oai.Client
	// The grokker version number this db was last updated with.
	Version string
	// The absolute path of the root directory of the document
	// repository.  This is passed in from cli based on where we
	// found the db.
	Root string
	// The list of documents in the database.
	Documents []*Document
	// The list of chunks in the database.
	Chunks []*Chunk
	// model specs
	models   *Models
	Model    string
	oaiModel string
	// XXX use a real tokenizer and replace maxChunkLen with tokenLimit.
	// tokenLimit int
	maxChunkLen          int
	maxEmbeddingChunkLen int
}

// New creates a new Grokker database.
func New(rootdir, model string) (g *Grokker, err error) {
	defer Return(&err)
	// ensure rootdir is absolute and exists
	rootdir, err = filepath.Abs(rootdir)
	Ck(err)
	_, err = os.Stat(rootdir)
	Ck(err)
	// create the db
	g = &Grokker{
		Root:    rootdir,
		Version: version,
	}
	// initialize other bits
	// XXX redundant with other areas where we call initModel followed by initClients
	err = g.initModel(model)
	Ck(err)
	g.initClients()
	return
}

// Load loads a Grokker database from an io.Reader.  The grokpath
// argument is the absolute path of the grok file.
// XXX rename this to LoadFile, don't pass in the reader.
func Load(r io.Reader, grokpath string, migrate bool) (g *Grokker, err error) {
	defer Return(&err)
	buf, err := ioutil.ReadAll(r)
	Ck(err)
	g = &Grokker{}
	err = json.Unmarshal(buf, g)
	Ck(err)
	// set the root directory, overriding whatever was in the db
	g.Root, err = filepath.Abs(filepath.Dir(grokpath))
	Ck(err)
	// set default version
	if g.Version == "" {
		g.Version = "0.1.0"
	}
	if migrate {
		// don't do anything else, just return the db for now
		// XXX we should call g.migrate() here instead, and
		// then continue on to the rest of the function.
		return
	}
	// check version
	if g.Version != version {
		Fpf(os.Stderr, "grokker db was created with version %s, but you're running version %s -- try `grok migrate`\n",
			g.Version, version)
		os.Exit(1)
	}
	// XXX redundant with other areas where we call initModel followed by initClients
	err = g.initModel(g.Model)
	Ck(err)
	g.initClients()
	return
}

// Migrate migrates the current Grokker database from an older version
// to the current version.
// XXX unexport this and call it from Load() after moving file ops
// into this package.
func (g *Grokker) Migrate() (was, now string, newgrok *Grokker, err error) {
	defer Return(&err)
	was = g.Version
	if g.Version == "0.1.0" {
		migrate_0_1_0_to_1_0_0(g)
	}
	// XXX remove doc.Path
	now = g.Version
	newgrok = g

	// refresh embeddings now because we are about to save the grok file
	// and that will make its timestamp newer than any possibly-modified
	// documents
	// XXX redundant with other areas where we call initModel followed by initClients
	err = g.initModel(g.Model)
	Ck(err)
	g.initClients()

	err = g.RefreshEmbeddings()
	Ck(err)

	return
}

// initClients initializes the OpenAI clients.
func (g *Grokker) initClients() {
	authtoken := os.Getenv("OPENAI_API_KEY")
	g.embeddingClient = openai.NewClient(authtoken)
	g.chatClient = oai.NewClient(authtoken)
	return
}

// initModel initializes the model for a new or reloaded Grokker database.
func (g *Grokker) initModel(model string) (err error) {
	defer Return(&err)
	Assert(g.Root != "", "root directory not set")
	g.models = NewModels()
	model, m, err := g.models.findModel(model)
	Ck(err)
	m.active = true
	g.Model = model
	g.oaiModel = m.oaiModel
	// XXX replace with a real tokenizer.
	charsPerToken := 3.1
	g.maxChunkLen = int(math.Floor(float64(m.TokenLimit) * charsPerToken))
	// XXX replace with a real tokenizer.
	// XXX 8192 hardcoded for the text-embedding-ada-002 model
	g.maxEmbeddingChunkLen = int(math.Floor(float64(8192) * charsPerToken))
	return
}

// UpgradeModel upgrades the model for a Grokker database.
func (g *Grokker) UpgradeModel(model string) (err error) {
	defer Return(&err)
	model, m, err := g.models.findModel(model)
	Ck(err)
	oldModel, oldM, err := g.getModel()
	Ck(err)
	// allow upgrade to a larger model, but not a smaller one
	if m.TokenLimit < oldM.TokenLimit {
		err = fmt.Errorf("cannot downgrade model from '%s' to '%s'", oldModel, model)
		return
	}
	g.initModel(model)
	return
}

// getModel returns the current model name and model_t from the db
func (g *Grokker) getModel() (model string, m *Model, err error) {
	defer Return(&err)
	model, m, err = g.models.findModel(g.Model)
	Ck(err)
	return
}

// Save saves a Grokker database as json data in an io.Writer.
func (g *Grokker) Save(w io.Writer) (err error) {
	defer Return(&err)
	data, err := json.Marshal(g)
	Ck(err)
	_, err = w.Write(data)
	return
}

// UpdateEmbeddings updates the embeddings for any documents that have
// changed since the last time the embeddings were updated.  It returns
// true if any embeddings were updated.
func (g *Grokker) UpdateEmbeddings(lastUpdate time.Time) (update bool, err error) {
	defer Return(&err)
	// we use the timestamp of the grokfn as the last embedding update time.
	for _, doc := range g.Documents {
		// check if the document has changed.
		fi, err := os.Stat(g.AbsPath(doc))
		if os.IsNotExist(err) {
			// document has been removed; remove it from the database.
			g.ForgetDocument(g.AbsPath(doc))
			update = true
			continue
		}
		Ck(err)
		if fi.ModTime().After(lastUpdate) {
			// update the embeddings.
			Debug("updating embeddings for %s ...", doc.RelPath)
			updated, err := g.UpdateDocument(doc)
			Ck(err)
			Debug("done\n")
			update = update || updated
		}
	}
	// garbage collect any chunks that are no longer referenced.
	g.GC()
	return
}

// AddDocument adds a document to the Grokker database. It creates the
// embeddings for the document and adds them to the database.
func (g *Grokker) AddDocument(path string) (err error) {
	defer Return(&err)
	// assume we're in an arbitrary directory, so we need to
	// convert the path to an absolute path.
	absPath, err := filepath.Abs(path)
	Ck(err)
	// always convert path to a relative path for consistency
	relpath, err := filepath.Rel(g.Root, absPath)
	doc := &Document{
		RelPath: relpath,
	}
	// ensure the document exists
	_, err = os.Stat(g.AbsPath(doc))
	if os.IsNotExist(err) {
		err = fmt.Errorf("not found: %s", doc.RelPath)
		return
	}
	Ck(err)
	// find out if the document is already in the database.
	found := false
	for _, d := range g.Documents {
		if d.RelPath == doc.RelPath {
			found = true
			break
		}
	}
	if !found {
		// add the document to the database.
		g.Documents = append(g.Documents, doc)
	}
	// update the embeddings for the document.
	_, err = g.UpdateDocument(doc)
	Ck(err)
	return
}

// ForgetDocument removes a document from the Grokker database.
func (g *Grokker) ForgetDocument(path string) (err error) {
	defer Return(&err)
	// remove the document from the database.
	for i, d := range g.Documents {
		match := false
		// try comparing the paths directly first.
		if d.RelPath == path {
			match = true
		}
		// if that doesn't work, try comparing the absolute paths.
		relpath, err := filepath.Abs(path)
		Ck(err)
		if g.AbsPath(d) == relpath {
			match = true
		}
		if match {
			Debug("forgetting document %s ...", path)
			g.Documents = append(g.Documents[:i], g.Documents[i+1:]...)
			break
		}
	}
	// the document chunks are still in the database, but they will be
	// removed during garbage collection.
	return
}

// GC removes any chunks that are no longer referenced by any document.
func (g *Grokker) GC() (err error) {
	defer Return(&err)
	// for each chunk, check if it is referenced by any document.
	// if not, remove it from the database.
	oldLen := len(g.Chunks)
	newChunks := make([]*Chunk, 0, len(g.Chunks))
	for _, chunk := range g.Chunks {
		// check if the chunk is referenced by any document.
		referenced := false
		for _, doc := range g.Documents {
			if doc.RelPath == chunk.Document.RelPath {
				referenced = true
				break
			}
		}
		if referenced {
			newChunks = append(newChunks, chunk)
		}
	}
	g.Chunks = newChunks
	newLen := len(g.Chunks)
	Debug("garbage collected %d chunks from the database", oldLen-newLen)
	return
}

// UpdateDocument updates the embeddings for a document and returns
// true if the document was updated.
func (g *Grokker) UpdateDocument(doc *Document) (updated bool, err error) {
	defer Return(&err)
	// XXX much of this code is inefficient and will be replaced
	// when we have a kv store.
	Debug("updating embeddings for %s ...", doc.RelPath)
	// break the doc up into chunks.
	chunkStrings, err := g.chunkStrings(doc)
	Ck(err)
	// get a list of the existing chunks for this document.
	var oldChunks []*Chunk
	var newChunkStrings []string
	for _, chunk := range g.Chunks {
		if chunk.Document.RelPath == doc.RelPath {
			oldChunks = append(oldChunks, chunk)
		}
	}
	Debug("found %d existing chunks", len(oldChunks))
	// for each chunk, check if it already exists in the database.
	for _, chunkString := range chunkStrings {
		found := false
		for _, oldChunk := range oldChunks {
			if oldChunk.Text == chunkString {
				// the chunk already exists in the database.  remove it from the list of old chunks.
				found = true
				for i, c := range oldChunks {
					if c == oldChunk {
						oldChunks = append(oldChunks[:i], oldChunks[i+1:]...)
						break
					}
				}
				break
			}
		}
		if !found {
			// the chunk does not exist in the database.  add it.
			updated = true
			newChunkStrings = append(newChunkStrings, chunkString)
		}
	}
	Debug("found %d new chunks", len(newChunkStrings))
	// orphaned chunks will be garbage collected.

	// For each text chunk, generate an embedding using the
	// openai.Embedding.create() function. Store the embeddings for each
	// chunk in a data structure such as a list or dictionary.
	embeddings, err := g.CreateEmbeddings(newChunkStrings)
	Ck(err)
	for i, text := range newChunkStrings {
		chunk := &Chunk{
			Document:  doc,
			Text:      text,
			Embedding: embeddings[i],
		}
		g.Chunks = append(g.Chunks, chunk)
	}
	return
}

// Embeddings returns the embeddings for a slice of text chunks.
func (g *Grokker) CreateEmbeddings(texts []string) (embeddings [][]float64, err error) {
	// use github.com/fabiustech/openai library
	c := g.embeddingClient
	// simply return an empty list if there are no texts.
	if len(texts) == 0 {
		return
	}
	// iterate over the text chunks and create one or more embedding queries
	for i := 0; i < len(texts); {
		// add texts to the current query until we reach the token limit
		// XXX use a real tokenizer
		// i is the index of the first text in the current query
		// j is the index of the last text in the current query
		// XXX this is ugly, fragile, and needs to be tested and refactored
		totalLen := 0
		j := i
		for {
			nextLen := len(texts[j])
			Debug("i=%d j=%d nextLen=%d totalLen=%d", i, j, nextLen, totalLen)
			Assert(nextLen > 0)
			Assert(nextLen <= g.maxEmbeddingChunkLen, "nextLen=%d maxEmbeddingChunkLen=%d", nextLen, g.maxEmbeddingChunkLen)
			if totalLen+nextLen >= g.maxEmbeddingChunkLen {
				j--
				Debug("breaking because totalLen=%d nextLen=%d", totalLen, nextLen)
				break
			}
			totalLen += nextLen
			if j == len(texts)-1 {
				Debug("breaking because j=%d len(texts)=%d", j, len(texts))
				break
			}
			j++
		}
		Debug("i=%d j=%d totalLen=%d", i, j, totalLen)
		Assert(j >= i, "j=%d i=%d", j, i)
		Assert(totalLen > 0, "totalLen=%d", totalLen)
		inputs := texts[i : j+1]
		// double-check that the total length is within the limit and that
		// no individual text is too long.
		totalLen = 0
		for _, text := range inputs {
			totalLen += len(text)
			Debug("len(text)=%d, totalLen=%d", len(text), totalLen)
			Assert(len(text) <= g.maxEmbeddingChunkLen, "text too long: %d", len(text))
		}
		Assert(totalLen <= g.maxEmbeddingChunkLen, "totalLen=%d maxEmbeddingChunkLen=%d", totalLen, g.maxEmbeddingChunkLen)
		req := &openai.EmbeddingRequest{
			Input: inputs,
			Model: fabius_models.AdaEmbeddingV2,
		}
		res, err := c.CreateEmbeddings(context.Background(), req)
		Ck(err)
		for _, em := range res.Data {
			embeddings = append(embeddings, em.Embedding)
		}
		i = j + 1
	}
	Debug("created %d embeddings", len(embeddings))
	Assert(len(embeddings) == len(texts))
	return
}

// chunkStrings returns a slice containing the chunk strings for a document.
func (g *Grokker) chunkStrings(doc *Document) (c []string, err error) {
	defer Return(&err)
	// read the document.
	buf, err := ioutil.ReadFile(g.AbsPath(doc))
	Ck(err)
	return g.chunks(string(buf), g.maxEmbeddingChunkLen), nil
}

// chunks returns a slice containing the chunk strings for a string.
func (g *Grokker) chunks(txt string, maxLen int) (c []string) {
	// Break up the text into smaller text chunks or passages, each
	// with a length of up to the limit for the model used by the
	// Embeddings API
	//
	// XXX splitting on paragraphs is not ideal.  smarter splitting
	// might look at the structure of the text and split on
	// sections, chapters, etc.  it might also be useful to include
	// metadata such as file names.
	paragraphs := strings.Split(string(txt), "\n\n")
	for _, paragraph := range paragraphs {
		// split the paragraph into chunks if it's too long.
		// XXX replace with a real tokenizer.
		for len(paragraph) > 0 {
			if len(paragraph) >= maxLen {
				split := maxLen - 1
				c = append(c, paragraph[:split])
				paragraph = paragraph[split:]
			} else {
				c = append(c, paragraph)
				paragraph = ""
			}
		}
	}
	return
}

// (4) When a question is asked, use the embeddings of the document
// chunks to find the most relevant passages for the question. You can
// use a similarity measure such as cosine similarity to compare the
// embeddings of the question and each document chunk, and return the
// chunks with the highest similarity scores.

// FindChunks returns the K most relevant chunks for a query.
func (g *Grokker) FindChunks(query string, K int) (chunks []*Chunk, err error) {
	defer Return(&err)
	// get the embeddings for the query.
	embeddings, err := g.CreateEmbeddings([]string{query})
	Ck(err)
	queryEmbedding := embeddings[0]
	// find the most similar chunks.
	chunks = g.SimilarChunks(queryEmbedding, K)
	return
}

// SimilarChunks returns the K most similar chunks to an embedding.
// If K is 0, it returns all chunks.
func (g *Grokker) SimilarChunks(embedding []float64, K int) (chunks []*Chunk) {
	Debug("chunks in database: %d", len(g.Chunks))
	// find the most similar chunks.
	type Sim struct {
		chunk *Chunk
		score float64
	}
	sims := make([]Sim, 0, len(g.Chunks))
	for _, chunk := range g.Chunks {
		score := Similarity(embedding, chunk.Embedding)
		sims = append(sims, Sim{chunk, score})
	}
	// sort the chunks by similarity.
	sort.Slice(sims, func(i, j int) bool {
		return sims[i].score > sims[j].score
	})
	// return the top K chunks.
	if K == 0 {
		K = len(sims)
	}
	for i := 0; i < K && i < len(sims); i++ {
		chunks = append(chunks, sims[i].chunk)
	}
	Debug("found %d similar chunks", len(chunks))
	return
}

// Similarity returns the cosine similarity between two embeddings.
func Similarity(a, b []float64) float64 {
	var dot, magA, magB float64
	for i := range a {
		dot += a[i] * b[i]
		magA += a[i] * a[i]
		magB += b[i] * b[i]
	}
	return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}

// (5) Provide the most relevant document chunks to the
// openai.Completion.create() function as additional context for
// generating a response. This will allow the model to better
// understand the context of the question and generate a more relevant
// response.

// Answer returns the answer to a question.
func (g *Grokker) Answer(question string, global bool) (resp oai.ChatCompletionResponse, query string, err error) {
	defer Return(&err)
	// get all chunks, sorted by similarity to the question.
	chunks, err := g.FindChunks(question, 0)
	Ck(err)
	// ensure the context is not too long.
	maxSize := int(float64(g.maxChunkLen)*0.5) - len(question)
	// use chunks as context for the answer until we reach the max size.
	var context string
	for _, chunk := range chunks {
		// context += chunk.Text + "\n\n"
		// include filename in context
		context += Spf("%s:\n\n%s\n\n", chunk.Document.RelPath, chunk.Text)
		// XXX promptTmpl doesn't appear to be in use atm
		if len(context)+len(promptTmpl) > maxSize {
			break
		}
	}
	Debug("using %d chunks as context", len(chunks))

	// generate the answer.
	resp, query, err = g.Generate(question, context, global)
	return
}

// Use the openai.Completion.create() function to generate a
// response to the question. You can use the prompt parameter to
// provide the question, and the max_tokens parameter to limit the
// length of the response.

// var promptTmpl = `You are a helpful assistant.  Answer the following question and summarize the context:
// var promptTmpl = `You are a helpful assistant.
var promptTmpl = `{{.Question}}

Context:
{{.Context}}`

// Generate returns the answer to a question.
func (g *Grokker) Generate(question, ctxt string, global bool) (resp oai.ChatCompletionResponse, query string, err error) {
	defer Return(&err)

	/*
		var systemText string
		if global {
			systemText = "You are a helpful assistant that provides answers from everything you know, as well as from the context provided in this chat."
		} else {
			systemText = "You are a helpful assistant that provides answers from the context provided in this chat."
		}
	*/

	// XXX don't exceed max tokens
	messages := []oai.ChatCompletionMessage{
		{
			Role:    oai.ChatMessageRoleSystem,
			Content: "You are a helpful assistant.",
		},
	}

	// first get global knowledge
	if global {
		messages = append(messages, oai.ChatCompletionMessage{
			Role:    oai.ChatMessageRoleUser,
			Content: question,
		})
		resp, err = g.chat(messages)
		Ck(err)
		// add the response to the messages.
		messages = append(messages, oai.ChatCompletionMessage{
			Role:    oai.ChatMessageRoleAssistant,
			Content: resp.Choices[0].Message.Content,
		})
	}

	// add context from local sources
	if len(ctxt) > 0 {
		messages = append(messages, []oai.ChatCompletionMessage{
			{
				Role:    oai.ChatMessageRoleUser,
				Content: Spf("first, some context:\n\n%s", ctxt),
			},
			{
				Role:    oai.ChatMessageRoleAssistant,
				Content: "Great! I've read the context.",
			},
		}...)
	}

	// now ask the question
	messages = append(messages, oai.ChatCompletionMessage{
		Role:    oai.ChatMessageRoleUser,
		Content: question,
	})

	// get the answer
	resp, err = g.chat(messages)
	Ck(err, "context length: %d", len(ctxt))

	// fmt.Println(resp.Choices[0].Message.Content)
	// Pprint(messages)
	// Pprint(resp)
	return
}

// chat uses the openai API to continue a conversation given a
// (possibly synthesized) message history.
func (g *Grokker) chat(messages []oai.ChatCompletionMessage) (resp oai.ChatCompletionResponse, err error) {
	defer Return(&err)

	model := g.oaiModel
	Debug("chat model: %s", model)
	Debug("chat: messages: %v", messages)

	// use 	"github.com/sashabaranov/go-openai"
	client := g.chatClient
	resp, err = client.CreateChatCompletion(
		context.Background(),
		oai.ChatCompletionRequest{
			Model:    model,
			Messages: messages,
		},
	)
	Ck(err, "%#v", messages)
	totalBytes := 0
	for _, msg := range messages {
		totalBytes += len(msg.Content)
	}
	totalBytes += len(resp.Choices[0].Message.Content)
	ratio := float64(totalBytes) / float64(resp.Usage.TotalTokens)
	Debug("total tokens: %d  char/token ratio: %.1f\n", resp.Usage.TotalTokens, ratio)
	return
}

// ListDocuments returns a list of all documents in the knowledge base.
// XXX this is a bit of a hack, since we're using the document name as
// the document ID.
// XXX this is also a bit of a hack since we're trying to make this
// work for multiple versions
func (g *Grokker) ListDocuments() (paths []string) {
	for _, doc := range g.Documents {
		path := doc.Path
		if g.Version == "1.0.0" {
			path = doc.RelPath
		}
		paths = append(paths, path)
	}
	return
}

// ListModels lists the available models.
func (g *Grokker) ListModels() (models []*Model, err error) {
	defer Return(&err)
	for _, model := range g.models.Available {
		models = append(models, model)
	}
	return
}

// RefreshEmbeddings refreshes the embeddings for all documents in the
// database.
func (g *Grokker) RefreshEmbeddings() (err error) {
	defer Return(&err)
	// regenerate the embeddings for each document.
	for _, doc := range g.Documents {
		Debug("refreshing embeddings for %s", doc.RelPath)
		// remove file from list if it doesn't exist.
		absPath := g.AbsPath(doc)
		Debug("absPath: %s", absPath)
		_, err := os.Stat(absPath)
		Debug("stat err: %v", err)
		if os.IsNotExist(err) {
			// remove the document from the database.
			g.ForgetDocument(doc.RelPath)
			continue
		}
		_, err = g.UpdateDocument(doc)
		Ck(err)
	}
	g.GC()
	return
}

var GitCommitPrompt = `
Summarize the bullet points found in the context into a single line of 60 characters or less.  Append a blank line, followed by the unaltered context.  Add nothing else.  Use present tense.
`

var GitDiffPrompt = `
Summarize the bullet points and 'git diff' fragments found in the context into bullet points to be used in the body of a git commit message.  Add nothing else. Use present tense. 
`

// GitCommitMessage generates a git commit message given a diff. It
// appends a reasonable prompt, and then uses the result as a grokker
// query.
func (g *Grokker) GitCommitMessage(diff string) (resp oai.ChatCompletionResponse, query string, err error) {
	defer Return(&err)

	// summarize the diff
	summary, err := g.summarizeDiff(diff)
	Ck(err)

	// XXX we are currently not providing additional context from the
	// embedded documents.  We should do that.

	// use the result as a grokker query
	// resp, query, err = g.Answer(prompt, false)
	resp, _, err = g.Generate(GitCommitPrompt, summary, false)
	Ck(err)
	return
}

// summarizeDiff recursively summarizes a diff until the summary is
// short enough to be used as a prompt.
func (g *Grokker) summarizeDiff(diff string) (diffSummary string, err error) {
	defer Return(&err)
	maxLen := int(float64(g.maxChunkLen) * .7)
	// split the diff on filenames
	fileChunks := strings.Split(diff, "diff --git")
	// split each file chunk into smaller chunks
	for _, fileChunk := range fileChunks {
		// get the filenames (they were right after the "diff --git"
		// string, on the same line)
		lines := strings.Split(fileChunk, "\n")
		var fns string
		if len(lines) > 0 {
			fns = lines[0]
		} else {
			fns = "a b"
		}
		var fileSummary string
		if len(fns) > 0 {
			fileSummary = Spf("summary of diff --git %s\n", fns)
		}
		chunks := g.chunks(fileChunk, maxLen)
		// summarize each chunk
		for _, chunk := range chunks {
			// format the chunk
			context := Spf("diff --git %s\n%s", fns, chunk)
			resp, _, err := g.Generate(GitDiffPrompt, context, false)
			Ck(err)
			fileSummary = Spf("%s\n%s", fileSummary, resp.Choices[0].Message.Content)
		}
		// prepend a summary line of the changes for this file
		resp, _, err := g.Generate(GitCommitPrompt, fileSummary, false)
		Ck(err)
		// append the summary of the changes for this file to the
		// summary of the changes for all files
		diffSummary = Spf("%s\n\n%s", diffSummary, resp.Choices[0].Message.Content)
	}
	if len(diffSummary) > int(maxLen) {
		// recurse
		Fpf(os.Stderr, "diff summary too long (%d bytes), recursing\n", len(diffSummary))
		diffSummary, err = g.summarizeDiff(diffSummary)
	}
	return
}

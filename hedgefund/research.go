// research.go is the third of the four hedge-fund agent objects: the
// research agent. It reads local PDF/text/markdown files from a
// configured directory and proposes candidate trading-model ideas
// grounded in their content -- same ReAct-agent-over-narrow-tools shape
// as risk.go/pnl.go, not the batch load-everything-then-one-Generate-call
// shape optimizer/../go-wiki/client/hedgefund's own research.go uses
// (that package's own README documents why these two hedge-fund builds
// deliberately took different approaches to the same problem).
//
// Uses eino's own document-parsing primitives rather than hand-rolling
// text extraction: eino core's parser.TextParser for .txt/.md, eino-ext's
// pure-Go PDF parser (github.com/ledongthuc/pdf underneath, no external
// binary needed) for .pdf.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino-ext/components/document/parser/pdf"
	"github.com/cloudwego/eino/components/document/parser"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

// researchPersona keeps this agent to exactly one job: read what's
// actually in the configured directory and propose ideas traceable to a
// real document -- never invent a model, never size/place a trade (that
// is the fund manager's job, once it exists, not this agent's), same
// "narrow, evidence-bound role" discipline riskPersona/pnlPersona already
// follow.
const researchPersona = `You are the research agent for a crypto/Solana trading fund.
Your job is to read local research papers/notes (via list_research_papers and
read_research_paper) and propose candidate trading models or strategy
refinements grounded in what they actually say -- never invent a model that
isn't traceable to a specific document you actually read. If a document
turns out to be irrelevant to trading, say so plainly rather than forcing a
proposal out of it.

You never place a trade, size a position, or claim a proposal is safe to run
-- that is the risk agent's and fund manager's job, not yours. When you
propose an idea, always cite which file it came from.`

type noResearchInput struct{}

type readPaperInput struct {
	Filename string `json:"filename" jsonschema:"description=Exact filename (as returned by list_research_papers), not a full path -- must be a file directly inside the configured research directory."`
}

// ResearchFindings is this node's structured output -- v1 returns the
// agent's prose answer in Summary, same incremental-scope choice
// RiskAssessment/PnlReport made; promoting this to typed, per-proposal
// fields (mirroring go-wiki/client/hedgefund's own ModelProposal) is the
// natural next step once this feeds a workflow field mapping instead of
// a human reading stdout.
type ResearchFindings struct {
	Summary string
}

// maxPaperChars caps how much of one document's extracted text
// read_research_paper returns in a single call -- without this, one long
// PDF could consume the model's entire context window by itself.
// Truncation is reported explicitly in the returned text (not silent),
// so the agent knows to treat a truncated read as partial evidence, not
// the whole document.
const maxPaperChars = 20_000

// researchExts is the set of extensions list_research_papers/
// read_research_paper ever consider -- anything else is silently
// skipped by the list, and explicitly rejected by the read (see
// readResearchPaperImpl), matching go-wiki/client/hedgefund's own
// loadLocalDocuments convention (.pdf/.txt/.md, flat directory, not
// recursive -- a research drop folder is expected to be flat).
var researchExts = map[string]bool{".pdf": true, ".txt": true, ".md": true}

func listResearchPapersImpl(papersDir string) (string, error) {
	entries, err := os.ReadDir(papersDir)
	if err != nil {
		return "", fmt.Errorf("read research directory %s: %w", papersDir, err)
	}
	var sb strings.Builder
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !researchExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		info, err := e.Info()
		size := int64(-1)
		if err == nil {
			size = info.Size()
		}
		fmt.Fprintf(&sb, "%s (%d bytes)\n", e.Name(), size)
		count++
	}
	if count == 0 {
		return fmt.Sprintf("no .pdf/.txt/.md files found directly in %s", papersDir), nil
	}
	return sb.String(), nil
}

// resolvePaperPath rejects anything that isn't a bare filename directly
// inside papersDir -- the model only ever sees filenames via
// list_research_papers' own output, but it's an LLM tool argument, not a
// trusted value, so this is checked the same way any other
// externally-influenced path would be, not assumed safe because it
// "should" only ever be a name we ourselves listed.
func resolvePaperPath(papersDir, filename string) (string, error) {
	if filename == "" {
		return "", fmt.Errorf("filename is required")
	}
	if strings.ContainsAny(filename, "/\\") || filename == "." || filename == ".." {
		return "", fmt.Errorf("filename must be a bare name inside the research directory, not a path: %q", filename)
	}
	full := filepath.Join(papersDir, filename)
	if filepath.Dir(full) != filepath.Clean(papersDir) {
		return "", fmt.Errorf("filename resolves outside the research directory: %q", filename)
	}
	return full, nil
}

// newResearchParser builds the same ExtParser shape go-wiki/client/
// hedgefund's loadLocalDocuments uses: eino-ext's real PDF parser for
// .pdf, eino core's built-in TextParser for .txt/.md.
func newResearchParser(ctx context.Context) (*parser.ExtParser, error) {
	pdfParser, err := pdf.NewPDFParser(ctx, &pdf.Config{})
	if err != nil {
		return nil, fmt.Errorf("create pdf parser: %w", err)
	}
	return parser.NewExtParser(ctx, &parser.ExtParserConfig{
		Parsers: map[string]parser.Parser{
			".pdf": pdfParser,
			".txt": parser.TextParser{},
			".md":  parser.TextParser{},
		},
	})
}

func readResearchPaperImpl(ctx context.Context, papersDir, filename string) (string, error) {
	full, err := resolvePaperPath(papersDir, filename)
	if err != nil {
		return "", err
	}
	if !researchExts[strings.ToLower(filepath.Ext(filename))] {
		return "", fmt.Errorf("unsupported file type %q (only .pdf/.txt/.md are readable)", filepath.Ext(filename))
	}

	f, err := os.Open(full)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", filename, err)
	}
	defer func() { _ = f.Close() }()

	p, err := newResearchParser(ctx)
	if err != nil {
		return "", err
	}
	docs, err := p.Parse(ctx, f, parser.WithURI(full))
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", filename, err)
	}

	var sb strings.Builder
	for _, d := range docs {
		sb.WriteString(d.Content)
		sb.WriteString("\n")
	}
	content := sb.String()
	if len(content) > maxPaperChars {
		return fmt.Sprintf("%s\n\n[... truncated at %d characters; %s is longer than this ...]", content[:maxPaperChars], maxPaperChars, filename), nil
	}
	return content, nil
}

func researchTools(papersDir string) ([]tool.BaseTool, error) {
	listPapers, err := utils.InferTool(
		"list_research_papers",
		"List every .pdf/.txt/.md file directly in the configured research directory (not recursive), with each file's size in bytes. Call this before read_research_paper to see what's available.",
		func(ctx context.Context, _ noResearchInput) (string, error) {
			return listResearchPapersImpl(papersDir)
		})
	if err != nil {
		return nil, fmt.Errorf("infer list_research_papers: %w", err)
	}

	readPaper, err := utils.InferTool(
		"read_research_paper",
		"Read one .pdf/.txt/.md file's full text content by filename, exactly as returned by list_research_papers. PDFs are parsed with a plain-text extractor -- layout/formatting is not preserved. Long documents are truncated with an explicit notice, not silently cut.",
		func(ctx context.Context, in readPaperInput) (string, error) {
			return readResearchPaperImpl(ctx, papersDir, in.Filename)
		})
	if err != nil {
		return nil, fmt.Errorf("infer read_research_paper: %w", err)
	}

	return []tool.BaseTool{listPapers, readPaper}, nil
}

// NewResearchAgent builds the research agent: a ReAct loop over
// researchTools, with researchPersona injected via
// react.NewPersonaModifier, same shape as risk.go's NewRiskAgent/pnl.go's
// NewPnlAgent.
func NewResearchAgent(ctx context.Context, cm model.ToolCallingChatModel, papersDir string) (*react.Agent, error) {
	tools, err := researchTools(papersDir)
	if err != nil {
		return nil, fmt.Errorf("hedgefund: build research tools: %w", err)
	}

	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: cm,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: tools},
		MessageModifier:  react.NewPersonaModifier(researchPersona),
	})
	if err != nil {
		return nil, fmt.Errorf("hedgefund: create research agent: %w", err)
	}
	return agent, nil
}

// RunResearch sends question to the research agent and returns its final
// answer once its tool-calling loop settles -- same shape as risk.go's
// RunRiskAssessment/pnl.go's RunPnlReport.
func RunResearch(ctx context.Context, agent *react.Agent, question string) (ResearchFindings, error) {
	msg, err := agent.Generate(ctx, []*schema.Message{schema.UserMessage(question)})
	if err != nil {
		return ResearchFindings{}, fmt.Errorf("hedgefund: research agent generate: %w", err)
	}
	return ResearchFindings{Summary: msg.Content}, nil
}

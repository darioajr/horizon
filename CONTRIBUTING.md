# Contribuindo para o Horizon

Obrigado pelo seu interesse em contribuir com o Horizon! Este documento fornece diretrizes e informações para contribuidores.

## Índice

1. [Código de Conduta](#código-de-conduta)
2. [Como Contribuir](#como-contribuir)
3. [Configuração do Ambiente](#configuração-do-ambiente)
4. [Padrões de Código](#padrões-de-código)
5. [Commits e Pull Requests](#commits-e-pull-requests)
6. [Testes](#testes)
7. [Documentação](#documentação)

---

## Código de Conduta

- Seja respeitoso e inclusivo
- Aceite críticas construtivas
- Foque no que é melhor para a comunidade
- Mostre empatia com outros membros

---

## Como Contribuir

### Reportando Bugs

1. Verifique se o bug já não foi reportado nas [Issues](../../issues)
2. Crie uma nova issue com:
   - Título claro e descritivo
   - Passos para reproduzir
   - Comportamento esperado vs. atual
   - Versão do Horizon
   - Sistema operacional e versão

### Sugerindo Melhorias

1. Abra uma issue com a tag `enhancement`
2. Descreva a melhoria proposta
3. Explique por que seria útil
4. Forneça exemplos se possível

### Contribuindo com Código

1. Fork o repositório
2. Crie uma branch para sua feature (`git checkout -b feature/minha-feature`)
3. Faça suas alterações
4. Adicione testes para novas funcionalidades
5. Certifique-se de que todos os testes passam
6. Commit suas mudanças (veja [Commits](#commits-e-pull-requests))
7. Push para sua branch (`git push origin feature/minha-feature`)
8. Abra um Pull Request

---

## Configuração do Ambiente

### Pré-requisitos

- Go 1.26+
- Git
- Docker Desktop (opcional, para testes de integração e benchmarks)

### Build

```bash
# Clonar
git clone https://github.com/darioajr/horizon.git
cd horizon

# Build para a plataforma atual
go build -o horizon ./cmd/horizon

# Build para todas as plataformas (via Make)
make build-all

# Build via Docker (cross-platform)
make docker-build

# Linux/macOS (via Bash)
./scripts/build.sh build
./scripts/build.sh docker-build

# Windows (via PowerShell)
.\scripts\build.ps1 -Target build
.\scripts\build.ps1 -Target docker-build
```

### Executar

```bash
# Executar diretamente
go run ./cmd/horizon

# Executar binário compilado
./dist/horizon-linux-amd64          # Linux
.\dist\horizon-windows-amd64.exe    # Windows

# Com advertised-host para Docker
./dist/horizon-linux-amd64 -advertised-host host.docker.internal
```

### Executar Testes

```bash
# Todos os testes
go test ./...

# Testes com verbose
go test -v ./...

# Testes de um pacote específico
go test -v ./internal/storage/

# Com cobertura
go test -cover ./...

# Linux/macOS (via Bash)
./scripts/build.sh test

# Windows (via PowerShell)
.\scripts\build.ps1 -Target test
```

---

## Padrões de Código

### Go 1.26

O projeto usa Go 1.26. Siga as convenções idiomáticas do Go:

```go
// ✅ Bom - usar errors.New para erros simples
var ErrTopicNotFound = errors.New("topic not found")

// ✅ Bom - retornar error como último valor
func (b *Broker) CreateTopic(name string, partitions int) error {
    // ...
}

// ✅ Bom - usar defer para cleanup
func (s *Segment) Close() error {
    defer s.mu.Unlock()
    s.mu.Lock()
    return s.file.Close()
}

// ✅ Bom - interfaces pequenas e focadas
type Reader interface {
    Read(p []byte) (n int, err error)
}
```

### Estilo de Código

- **Formatação**: Use `gofmt` (automático)
- **Linting**: `go vet ./...`
- **Imports**: Agrupe stdlib, depois dependências externas, depois pacotes internos

```go
import (
    "fmt"
    "sync"

    "gopkg.in/yaml.v3"

    "horizon/internal/broker"
    "horizon/internal/storage"
)
```

### Nomenclatura

| Tipo | Convenção | Exemplo |
|------|-----------|---------|
| Tipos exportados | PascalCase | `ConsumerGroup` |
| Tipos internos | camelCase | `consumerState` |
| Funções exportadas | PascalCase | `NewBroker()` |
| Funções internas | camelCase | `handleRequest()` |
| Variáveis | camelCase | `partitionCount` |
| Constantes | PascalCase ou camelCase | `MaxBatchSize` |
| Interfaces | PascalCase (sem prefixo I) | `Writer` |
| Pacotes | lowercase | `storage` |

### Organização de Arquivos

```go
package server

import (
    // imports agrupados
)

// Constantes e variáveis de pacote
const defaultPort = 9092

// Tipos
type Server struct {
    // campos
}

// Construtores
func NewServer(config ServerConfig) *Server {
    // ...
}

// Métodos exportados
func (s *Server) Start() error {
    // ...
}

// Métodos internos
func (s *Server) handleConnection(conn net.Conn) {
    // ...
}
```

### Tratamento de Erros

- Retorne `error` como último valor de retorno
- Use erros sentinela para erros conhecidos
- Envolva erros com `fmt.Errorf("contexto: %w", err)` para adicionar contexto
- Use códigos de erro do Kafka para respostas do protocolo

```go
// ✅ Bom - erros sentinela
var (
    ErrTopicNotFound    = errors.New("topic not found")
    ErrPartitionFull    = errors.New("partition full")
)

// ✅ Bom - wrap de erros
func (p *Partition) Append(data []byte) (int64, error) {
    offset, err := p.activeSegment.Append(data)
    if err != nil {
        return 0, fmt.Errorf("append to partition %d: %w", p.id, err)
    }
    return offset, nil
}
```

---

## Commits e Pull Requests

### Mensagens de Commit

Usamos [Conventional Commits](https://www.conventionalcommits.org/):

```
<tipo>(<escopo>): <descrição>

[corpo opcional]

[rodapé opcional]
```

#### Tipos

| Tipo | Descrição |
|------|-----------|
| `feat` | Nova funcionalidade |
| `fix` | Correção de bug |
| `docs` | Apenas documentação |
| `style` | Formatação, sem mudança de código |
| `refactor` | Refatoração de código |
| `perf` | Melhoria de performance |
| `test` | Adição ou correção de testes |
| `chore` | Tarefas de manutenção |
| `ci` | Mudanças no CI/CD |

#### Exemplos

```
feat(broker): add DeleteRecords API support

Implement Kafka DeleteRecords API (key 21) for clearing topic messages.
- Add handleDeleteRecords() in server handler
- Add DeleteRecordsBefore() in Partition
- Add Truncate() for full partition cleanup

Closes #42
```

```
fix(server): resolve rebalance timeout issue

The consumer group was timing out during rebalance when
members exceeded 30 seconds to rejoin.

Fixes #123
```

### Pull Requests

1. **Título**: Use o formato de commit convencional
2. **Descrição**: Explique o que foi feito e por quê
3. **Checklist**:
   - [ ] Testes adicionados/atualizados
   - [ ] Documentação atualizada
   - [ ] Código segue os padrões do projeto
   - [ ] `go vet ./...` sem warnings
   - [ ] `go test ./...` passa
   - [ ] Código formatado com `gofmt`

---

## Testes

### Estrutura de Testes

Os testes ficam junto aos arquivos fonte (`*_test.go`) e usam o pacote `testing` da stdlib:

```go
package storage

import (
    "os"
    "testing"
)

func TestRecordBatchEncodeDecode(t *testing.T) {
    // Arrange
    records := []Record{
        {Key: []byte("key1"), Value: []byte("value1")},
        {Key: []byte("key2"), Value: []byte("value2")},
    }

    // Act
    batch := NewRecordBatch(records)
    encoded := batch.Encode()
    decoded, err := DecodeRecordBatch(encoded)

    // Assert
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(decoded.Records) != len(records) {
        t.Errorf("expected %d records, got %d", len(records), len(decoded.Records))
    }
}

func TestPartitionAppendFetch(t *testing.T) {
    dir := t.TempDir()

    partition, err := NewPartition(dir, 0)
    if err != nil {
        t.Fatalf("failed to create partition: %v", err)
    }
    defer partition.Close()

    // Test append and fetch...
}
```

### Executar Testes Específicos

```bash
# Por pacote
go test -v ./internal/storage/

# Por nome
go test -v -run TestRecordBatch ./internal/storage/

# Por padrão
go test -v -run ".*Partition.*" ./internal/storage/

# Com race detector
go test -race ./...
```

### Cobertura

```bash
# Gerar relatório de cobertura
go test -coverprofile=coverage.out ./...

# Visualizar no terminal
go tool cover -func=coverage.out

# Visualizar no navegador
go tool cover -html=coverage.out
```

### Cobertura Mínima

- Novas funcionalidades devem ter testes
- Correções de bugs devem incluir teste que reproduz o bug
- Objetivo: manter cobertura acima de 80%

---

## Documentação

### Código

Use comentários GoDoc para APIs exportadas:

```go
// DeleteRecordsBefore deletes all records before the specified offset.
// Use offset -1 to delete up to the high watermark.
// Returns the new low watermark after deletion.
func (p *Partition) DeleteRecordsBefore(offset int64) (int64, error) {
    // ...
}

// Broker manages topics and partitions.
// It is safe for concurrent use.
type Broker struct {
    // ...
}
```

### README e Docs

- Mantenha o README.md atualizado
- Documente novas features
- Adicione exemplos de uso

---

## Arquitetura

### Estrutura do Projeto

```
horizon/
├── cmd/
│   └── horizon/
│       └── main.go           # Entry point
├── internal/
│   ├── broker/               # Broker e gerenciamento de tópicos
│   ├── config/               # Configuração (YAML)
│   ├── protocol/             # Protocolo Kafka (reader/writer)
│   ├── server/               # Servidor TCP e handlers
│   └── storage/              # Persistência (log, partições, segmentos)
├── build/                    # Dockerfile
├── configs/                  # Arquivos de configuração
├── deployments/              # Docker Compose
├── scripts/                  # Scripts de build e teste
├── benchmarks/               # Benchmarks de performance
└── data/                     # Dados persistidos (runtime)
```

### Componentes Principais

| Componente | Pacote | Responsabilidade |
|------------|--------|------------------|
| `Broker` | `internal/broker` | Gerenciamento de tópicos e partições |
| `Log` | `internal/storage` | Armazenamento de mensagens por tópico |
| `Partition` | `internal/storage` | Partição com segmentos de log |
| `Segment` | `internal/storage` | Segmentos de log no disco |
| `Server` | `internal/server` | Servidor TCP e dispatch de requests |
| `Handler` | `internal/server` | Handlers do protocolo Kafka |
| `Reader/Writer` | `internal/protocol` | Serialização do protocolo Kafka |

---

## Dúvidas?

- Abra uma [Discussion](../../discussions) para perguntas gerais
- Abra uma [Issue](../../issues) para bugs ou features
- Entre em contato com os maintainers

**Obrigado por contribuir!** 🚀

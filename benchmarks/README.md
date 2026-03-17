# Benchmark de Performance: Horizon vs Apache Kafka

Este diretório contém ferramentas para medir e comparar performance de produção
entre Horizon e Apache Kafka.

## Opções de Benchmark

### 1. OpenMessaging Benchmark (Recomendado)

Usa a ferramenta oficial da Confluent ([OpenMessaging Benchmark](https://github.com/confluentinc/openmessaging-benchmark))
via Docker. Mesma metodologia usada nos benchmarks oficiais do Kafka.

```powershell
# Comparar Horizon vs Kafka
.\run-benchmark.ps1 -Target compare

# Apenas Kafka
.\run-benchmark.ps1 -Target kafka

# Apenas Horizon (deve estar rodando em localhost:9092)
.\run-benchmark.ps1 -Target horizon

# Teste de latência
.\run-benchmark.ps1 -Target compare -Workload latency

# Customizar quantidade de mensagens
.\run-benchmark.ps1 -Target compare -MessageCount 500000

# Customizar tamanho da mensagem
.\run-benchmark.ps1 -Target compare -MessageSize 4096
```

### 2. Python Script (Testes Rápidos)

Script Python simples usando `confluent-kafka` e `testcontainers`.

```powershell
# Instalar dependências
pip install -r requirements.txt

# Comparar (Horizon deve estar rodando)
python produce_benchmark.py --compare --eh-host localhost --eh-port 9092

# Apenas Kafka (usa testcontainer)
python produce_benchmark.py --kafka-only

# Apenas Horizon
python produce_benchmark.py --horizon-only --eh-host localhost --eh-port 9092

# Customizar
python produce_benchmark.py --compare --messages 1000000 --message-size 1024
```

## Pré-requisitos

1. **Docker Desktop** ou **Podman** rodando
2. **Horizon** rodando em `localhost:9092` com `advertised-host` configurado para Docker/Podman

```powershell
# Iniciar Horizon para benchmark com Docker/Podman (em outro terminal)
cd ..
.\dist\horizon-windows-amd64.exe -advertised-host host.docker.internal
```

> **Importante**: O flag `-advertised-host host.docker.internal` é necessário porque o container Docker/Podman precisa
> se conectar ao host Windows. Sem isso, o benchmark não consegue conectar ao broker.

3. **Python 3.10+** (para script Python)

## Workloads

| Arquivo | Descrição |
|---------|-----------|
| `throughput-1kb.yaml` | Throughput máximo com mensagens de 1KB |
| `latency-1kb.yaml` | Latência em carga fixa (200K msg/s) |

## Métricas Coletadas

- **Throughput**: mensagens/segundo, MB/segundo
- **Latência**: P50, P90, P95, P99, P99.9, Max

## Configuração do Benchmark

Referência: https://developer.confluent.io/learn/kafka-performance/

O benchmark usa a ferramenta `kafka-producer-perf-test` (nativa do Kafka) com as seguintes configurações:

| Parâmetro | Throughput | Latency |
|-----------|------------|---------|
| `batch.size` | 1MB | 1MB |
| `linger.ms` | 10 | 1 |
| `acks` | all | all |
| `partitions` | 8 | 8 |
| `message size` | 1KB | 1KB |

## Resultados

Os resultados são salvos em `./results/`:
- `kafka-producer-benchmark.txt` - Resultados do Kafka
- `horizon-producer-benchmark.txt` - Resultados do Horizon

### Exemplo de Saída

```
100000 records sent, 125000.0 records/sec (122.07 MB/sec), 5.2 ms avg latency, 120.0 ms max latency, 4 ms 50th, 8 ms 95th, 15 ms 99th, 45 ms 99.9th.
```

## Arquivos

| Arquivo | Descrição |
|---------|-----------|
| `run-benchmark.ps1` | Script principal de benchmark |
| `produce_benchmark.py` | Benchmark Python com rich output |
| `requirements.txt` | Dependências Python |
| `docker-compose-benchmark.yml` | Configuração Docker/Podman para Kafka e benchmarks |
| `Dockerfile.benchmark` | OpenMessaging Benchmark builder |
| `drivers/` | Configs de driver OpenMessaging |
| `workloads/` | Workloads de benchmark |
| `results/` | Diretório com resultados |

## Interpretação dos Resultados

- **records/sec**: Throughput em mensagens por segundo
- **MB/sec**: Throughput em megabytes por segundo
- **avg latency**: Latência média em milissegundos
- **max latency**: Latência máxima observada
- **50th, 95th, 99th, 99.9th**: Percentis de latência

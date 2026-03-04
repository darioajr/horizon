#!/usr/bin/env python3
"""
Benchmark de Performance: Horizon vs Apache Kafka
Testa throughput e latência de produção de mensagens

Uso:
    python produce_benchmark.py --help
    python produce_benchmark.py --kafka-only
    python produce_benchmark.py --remote --host horizon-test --port 9092
    python produce_benchmark.py --remote --host kafka-kraft-test --port 9092
    python produce_benchmark.py --compare --host horizon-test --port 9092

Baseado em: https://developer.confluent.io/learn/kafka-performance/
"""

import argparse
import json
import os
import statistics
import sys
import time
from dataclasses import dataclass, field
from datetime import datetime
from typing import Optional

import numpy as np
from confluent_kafka import Producer, KafkaError, KafkaException
from confluent_kafka.admin import AdminClient, NewTopic
from rich.console import Console
from rich.table import Table
from rich.progress import Progress, SpinnerColumn, TextColumn, BarColumn, TaskProgressColumn

console = Console()


@dataclass
class BenchmarkConfig:
    """Configuração do benchmark"""
    topic: str = "benchmark-topic"
    num_partitions: int = 8
    replication_factor: int = 1
    
    # Mensagens
    message_size: int = 1024  # 1KB
    num_messages: int = 100_000  # 100K mensagens
    
    # Producer config (baseado no benchmark da Confluent)
    batch_size: int = 1_048_576  # 1MB
    linger_ms: int = 10  # 10ms para throughput
    acks: str = "all"  # -1 = all
    
    # Warmup
    warmup_messages: int = 1000


@dataclass
class ProduceResult:
    """Resultado do benchmark de produção"""
    target: str  # "kafka" ou "horizon"
    total_messages: int = 0
    total_bytes: int = 0
    duration_seconds: float = 0
    
    # Throughput
    messages_per_second: float = 0
    mb_per_second: float = 0
    
    # Latência (ms)
    latencies_ms: list = field(default_factory=list)
    latency_min: float = 0
    latency_max: float = 0
    latency_avg: float = 0
    latency_p50: float = 0
    latency_p90: float = 0
    latency_p95: float = 0
    latency_p99: float = 0
    latency_p999: float = 0
    
    # Erros
    errors: int = 0
    
    def compute_stats(self):
        """Calcula estatísticas de latência"""
        if not self.latencies_ms:
            return
        
        arr = np.array(self.latencies_ms)
        self.latency_min = float(np.min(arr))
        self.latency_max = float(np.max(arr))
        self.latency_avg = float(np.mean(arr))
        self.latency_p50 = float(np.percentile(arr, 50))
        self.latency_p90 = float(np.percentile(arr, 90))
        self.latency_p95 = float(np.percentile(arr, 95))
        self.latency_p99 = float(np.percentile(arr, 99))
        self.latency_p999 = float(np.percentile(arr, 99.9))
        
        if self.duration_seconds > 0:
            self.messages_per_second = self.total_messages / self.duration_seconds
            self.mb_per_second = (self.total_bytes / 1_048_576) / self.duration_seconds


def generate_payload(size: int) -> bytes:
    """Gera payload de tamanho fixo"""
    # Timestamp no início para medir latência E2E se necessário
    timestamp = str(int(time.time() * 1_000_000)).encode()
    padding_size = size - len(timestamp) - 1
    if padding_size < 0:
        padding_size = 0
    return timestamp + b"|" + (b"x" * padding_size)


def create_producer(bootstrap_servers: str, config: BenchmarkConfig) -> Producer:
    """Cria producer configurado para benchmark"""
    return Producer({
        "bootstrap.servers": bootstrap_servers,
        "client.id": "benchmark-producer",
        "acks": config.acks,
        "batch.size": config.batch_size,
        "linger.ms": config.linger_ms,
        "compression.type": "none",
        "retries": 0,  # Sem retries para medir performance real
        "queue.buffering.max.messages": 1_000_000,
        "queue.buffering.max.kbytes": 1_048_576,  # 1GB buffer
    })


def create_topic(admin_client: AdminClient, config: BenchmarkConfig):
    """Cria tópico de benchmark"""
    topic = NewTopic(
        config.topic,
        num_partitions=config.num_partitions,
        replication_factor=config.replication_factor
    )
    
    try:
        futures = admin_client.create_topics([topic])
        for topic_name, future in futures.items():
            try:
                future.result()
                console.print(f"[green]✓[/green] Tópico '{topic_name}' criado")
            except KafkaException as e:
                if e.args[0].code() == KafkaError.TOPIC_ALREADY_EXISTS:
                    console.print(f"[yellow]![/yellow] Tópico '{topic_name}' já existe")
                else:
                    raise
    except Exception as e:
        console.print(f"[red]✗[/red] Erro ao criar tópico: {e}")


def delete_topic(admin_client: AdminClient, topic: str):
    """Remove tópico de benchmark"""
    try:
        futures = admin_client.delete_topics([topic])
        for topic_name, future in futures.items():
            try:
                future.result()
            except:
                pass
    except:
        pass


def run_produce_benchmark(
    bootstrap_servers: str,
    config: BenchmarkConfig,
    target_name: str
) -> ProduceResult:
    """Executa benchmark de produção"""
    result = ProduceResult(target=target_name)
    
    # Admin client para gerenciar tópico
    admin = AdminClient({"bootstrap.servers": bootstrap_servers})
    
    # Limpar tópico anterior e criar novo
    delete_topic(admin, config.topic)
    time.sleep(1)
    create_topic(admin, config)
    time.sleep(2)  # Aguardar propagação
    
    # Criar producer
    producer = create_producer(bootstrap_servers, config)
    
    # Payload
    payload = generate_payload(config.message_size)
    
    # Callback para tracking
    delivery_times = []
    errors = [0]
    send_times = []  # parallel list of send timestamps
    delivery_idx = [0]  # index consumed by callback
    
    def delivery_callback(err, msg):
        if err:
            errors[0] += 1
        else:
            idx = delivery_idx[0]
            if idx < len(send_times):
                latency_ms = (time.perf_counter() - send_times[idx]) * 1000
                delivery_times.append(latency_ms)
            delivery_idx[0] += 1
    
    # Warmup
    console.print(f"\n[cyan]Warmup ({config.warmup_messages} mensagens)...[/cyan]")
    for _ in range(config.warmup_messages):
        send_times.append(time.perf_counter())
        producer.produce(
            config.topic,
            value=payload,
            callback=delivery_callback,
        )
        producer.poll(0)
    producer.flush()
    delivery_times.clear()
    send_times.clear()
    delivery_idx[0] = 0
    errors[0] = 0
    
    # Benchmark real
    console.print(f"\n[cyan]Produzindo {config.num_messages:,} mensagens ({config.message_size} bytes cada)...[/cyan]")
    
    start_time = time.perf_counter()
    
    with Progress(
        SpinnerColumn(),
        TextColumn("[progress.description]{task.description}"),
        BarColumn(),
        TaskProgressColumn(),
        console=console,
    ) as progress:
        task = progress.add_task(f"[green]Produzindo para {target_name}...", total=config.num_messages)
        
        for i in range(config.num_messages):
            send_times.append(time.perf_counter())
            producer.produce(
                config.topic,
                value=payload,
                callback=delivery_callback,
            )
            
            # Poll para processar callbacks
            if i % 1000 == 0:
                producer.poll(0)
                progress.update(task, completed=i)
        
        progress.update(task, completed=config.num_messages)
    
    # Flush final
    console.print("[cyan]Aguardando confirmações...[/cyan]")
    producer.flush(timeout=60)
    
    end_time = time.perf_counter()
    
    # Resultados
    result.total_messages = config.num_messages
    result.total_bytes = config.num_messages * config.message_size
    result.duration_seconds = end_time - start_time
    result.latencies_ms = delivery_times
    result.errors = errors[0]
    result.compute_stats()
    
    # Cleanup
    delete_topic(admin, config.topic)
    
    return result


def print_result(result: ProduceResult):
    """Exibe resultados formatados"""
    table = Table(title=f"Resultados: {result.target}", show_header=True, header_style="bold cyan")
    
    table.add_column("Métrica", style="dim")
    table.add_column("Valor", justify="right")
    
    table.add_row("Mensagens", f"{result.total_messages:,}")
    table.add_row("Bytes", f"{result.total_bytes / 1_048_576:.2f} MB")
    table.add_row("Duração", f"{result.duration_seconds:.2f} s")
    table.add_row("", "")
    table.add_row("[bold]Throughput[/bold]", "")
    table.add_row("  Mensagens/s", f"[green]{result.messages_per_second:,.0f}[/green]")
    table.add_row("  MB/s", f"[green]{result.mb_per_second:.2f}[/green]")
    table.add_row("", "")
    table.add_row("[bold]Latência (ms)[/bold]", "")
    table.add_row("  Min", f"{result.latency_min:.3f}")
    table.add_row("  Avg", f"{result.latency_avg:.3f}")
    table.add_row("  P50", f"{result.latency_p50:.3f}")
    table.add_row("  P90", f"{result.latency_p90:.3f}")
    table.add_row("  P95", f"{result.latency_p95:.3f}")
    table.add_row("  P99", f"[yellow]{result.latency_p99:.3f}[/yellow]")
    table.add_row("  P99.9", f"[yellow]{result.latency_p999:.3f}[/yellow]")
    table.add_row("  Max", f"{result.latency_max:.3f}")
    table.add_row("", "")
    table.add_row("Erros", f"[red]{result.errors}[/red]" if result.errors > 0 else "0")
    
    console.print(table)


def print_comparison(kafka_result: ProduceResult, horizon_result: ProduceResult):
    """Exibe comparação lado a lado"""
    table = Table(title="Comparação: Kafka vs Horizon", show_header=True, header_style="bold magenta")
    
    table.add_column("Métrica", style="dim")
    table.add_column("Kafka", justify="right")
    table.add_column("Horizon", justify="right")
    table.add_column("Diferença", justify="right")
    
    def diff(k, e, higher_is_better=True):
        if k == 0:
            return "N/A"
        pct = ((e - k) / k) * 100
        if higher_is_better:
            color = "green" if pct > 0 else "red"
        else:
            color = "green" if pct < 0 else "red"
        sign = "+" if pct > 0 else ""
        return f"[{color}]{sign}{pct:.1f}%[/{color}]"
    
    table.add_row("[bold]Throughput[/bold]", "", "", "")
    table.add_row(
        "  Mensagens/s",
        f"{kafka_result.messages_per_second:,.0f}",
        f"{horizon_result.messages_per_second:,.0f}",
        diff(kafka_result.messages_per_second, horizon_result.messages_per_second)
    )
    table.add_row(
        "  MB/s",
        f"{kafka_result.mb_per_second:.2f}",
        f"{horizon_result.mb_per_second:.2f}",
        diff(kafka_result.mb_per_second, horizon_result.mb_per_second)
    )
    table.add_row("", "", "", "")
    table.add_row("[bold]Latência (ms)[/bold]", "", "", "")
    table.add_row(
        "  P50",
        f"{kafka_result.latency_p50:.3f}",
        f"{horizon_result.latency_p50:.3f}",
        diff(kafka_result.latency_p50, horizon_result.latency_p50, higher_is_better=False)
    )
    table.add_row(
        "  P99",
        f"{kafka_result.latency_p99:.3f}",
        f"{horizon_result.latency_p99:.3f}",
        diff(kafka_result.latency_p99, horizon_result.latency_p99, higher_is_better=False)
    )
    table.add_row(
        "  P99.9",
        f"{kafka_result.latency_p999:.3f}",
        f"{horizon_result.latency_p999:.3f}",
        diff(kafka_result.latency_p999, horizon_result.latency_p999, higher_is_better=False)
    )
    
    console.print(table)


def save_results(results: list[ProduceResult], filename: str):
    """Salva resultados em JSON"""
    data = {
        "timestamp": datetime.now().isoformat(),
        "results": []
    }
    
    for r in results:
        data["results"].append({
            "target": r.target,
            "total_messages": r.total_messages,
            "total_bytes": r.total_bytes,
            "duration_seconds": r.duration_seconds,
            "throughput": {
                "messages_per_second": r.messages_per_second,
                "mb_per_second": r.mb_per_second
            },
            "latency_ms": {
                "min": r.latency_min,
                "avg": r.latency_avg,
                "p50": r.latency_p50,
                "p90": r.latency_p90,
                "p95": r.latency_p95,
                "p99": r.latency_p99,
                "p999": r.latency_p999,
                "max": r.latency_max
            },
            "errors": r.errors
        })
    
    with open(filename, "w") as f:
        json.dump(data, f, indent=2)
    
    console.print(f"[green]✓[/green] Resultados salvos em: {filename}")


def main():
    parser = argparse.ArgumentParser(
        description="Benchmark de produção: Horizon vs Apache Kafka",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Exemplos:
  python produce_benchmark.py --kafka-only
  python produce_benchmark.py --remote --host horizon-test --port 9092
  python produce_benchmark.py --remote --host kafka-kraft-test --port 9092
  python produce_benchmark.py --compare --host horizon-test --port 9092
  python produce_benchmark.py --compare --messages 1000000 --message-size 1024
        """
    )
    
    # Modo de execução
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--kafka-only", action="store_true", help="Testar apenas Kafka (via testcontainer, requer Docker no host)")
    mode.add_argument("--remote", action="store_true", help="Testar broker remoto (Horizon, Kafka, ou qualquer broker Kafka-compatível)")
    mode.add_argument("--compare", action="store_true", help="Comparar Kafka (testcontainer) vs broker remoto")
    
    # Broker remoto
    parser.add_argument("--host", default="localhost", help="Host do broker remoto (default: localhost)")
    parser.add_argument("--port", type=int, default=9092, help="Porta do broker remoto (default: 9092)")
    parser.add_argument("--name", default=None, help="Nome do broker para exibição (default: auto-detect do host)")
    
    # Configuração do teste
    parser.add_argument("--messages", type=int, default=100_000, help="Número de mensagens (default: 100000)")
    parser.add_argument("--message-size", type=int, default=1024, help="Tamanho da mensagem em bytes (default: 1024)")
    parser.add_argument("--partitions", type=int, default=8, help="Número de partições (default: 8)")
    parser.add_argument("--batch-size", type=int, default=1_048_576, help="Batch size em bytes (default: 1MB)")
    parser.add_argument("--linger-ms", type=int, default=10, help="Linger ms (default: 10)")
    parser.add_argument("--acks", default="all", choices=["0", "1", "all"], help="Acks (default: all)")
    
    # Output
    parser.add_argument("--output", "-o", help="Arquivo JSON para salvar resultados")
    
    args = parser.parse_args()
    
    # Configuração
    config = BenchmarkConfig(
        num_messages=args.messages,
        message_size=args.message_size,
        num_partitions=args.partitions,
        batch_size=args.batch_size,
        linger_ms=args.linger_ms,
        acks=args.acks
    )
    
    results = []
    kafka_result = None
    horizon_result = None
    
    console.print("\n[bold blue]═══════════════════════════════════════════════════════════[/bold blue]")
    console.print("[bold blue]  Benchmark de Produção: Horizon vs Apache Kafka[/bold blue]")
    console.print("[bold blue]═══════════════════════════════════════════════════════════[/bold blue]\n")
    
    console.print(f"[dim]Configuração:[/dim]")
    console.print(f"  Mensagens: {config.num_messages:,}")
    console.print(f"  Tamanho: {config.message_size} bytes")
    console.print(f"  Partições: {config.num_partitions}")
    console.print(f"  Batch size: {config.batch_size / 1024:.0f} KB")
    console.print(f"  Linger ms: {config.linger_ms}")
    console.print(f"  Acks: {config.acks}")
    
    # Testar Kafka
    if args.kafka_only or args.compare:
        console.print("\n[bold cyan]━━━ Apache Kafka (Testcontainer) ━━━[/bold cyan]")
        
        from testcontainers.kafka import KafkaContainer
        with KafkaContainer("confluentinc/cp-kafka:8.1.1") as kafka:
            bootstrap_servers = kafka.get_bootstrap_server()
            console.print(f"[green]✓[/green] Kafka iniciado: {bootstrap_servers}")
            
            kafka_result = run_produce_benchmark(bootstrap_servers, config, "Apache Kafka")
            results.append(kafka_result)
            print_result(kafka_result)
    
    # Testar broker remoto
    if args.remote or args.compare:
        # Auto-detect display name from host if not specified
        if args.name:
            target_name = args.name
        elif "horizon" in args.host.lower():
            target_name = "Horizon"
        elif "kafka" in args.host.lower():
            target_name = "Apache Kafka"
        else:
            target_name = args.host
        
        bootstrap_servers = f"{args.host}:{args.port}"
        console.print(f"\n[bold cyan]━━━ {target_name} ({bootstrap_servers}) ━━━[/bold cyan]")
        
        try:
            horizon_result = run_produce_benchmark(bootstrap_servers, config, target_name)
            results.append(horizon_result)
            print_result(horizon_result)
        except Exception as e:
            console.print(f"[red]✗[/red] Erro ao conectar a {target_name}: {e}")
            console.print(f"[yellow]Certifique-se de que {target_name} está rodando em {bootstrap_servers}.[/yellow]")
            sys.exit(1)
    
    # Comparação
    if args.compare and kafka_result and horizon_result:
        console.print("\n")
        print_comparison(kafka_result, horizon_result)
    
    # Salvar resultados
    if args.output:
        save_results(results, args.output)
    
    console.print("\n[bold green]Benchmark concluído![/bold green]\n")


if __name__ == "__main__":
    main()

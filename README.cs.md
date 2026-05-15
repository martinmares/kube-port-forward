# kube-port-forward

CLI wrapper kolem `kubectl port-forward`, který se automaticky znovu připojí při výpadku spojení.

## Problém

`kubectl port-forward` občas spadne s chybou:

```
Forwarding from 127.0.0.1:9090 -> 9090
Handling connection for 9090
error: lost connection to pod
```

V takovém případě je nutné příkaz ručně restartovat. Tento wrapper to udělá za vás.

## Instalace

Build ze zdrojových kódů (vyžaduje Go 1.21+):

```bash
go build -o kube-port-forward .
```

Volitelná globální instalace:

```bash
sudo cp kube-port-forward /usr/local/bin/
```

## Použití

```
kube-port-forward [OPTIONS] -- <kubectl port-forward argumenty...>
```

Subpříkaz `port-forward` se přidá automaticky — neuvádějte ho v argumentech.

### Přepínače

| Přepínač | Výchozí | Popis |
|----------|---------|-------|
| `--retries` | `always` | Celkový počet pokusů: kladné celé číslo nebo `always` pro nekonečno |
| `--retry-interval` | `5` | Počet sekund čekání mezi pokusy o reconnect |
| `--log-level` | `info` | Úroveň logování: `debug`, `info`, `warn`, `error` |

### Příklady

Přesměrování portu podu (při výpadku se automaticky znovu připojí):
```bash
kube-port-forward -- -n nac-app pod/nac-out-6c7985556d-h2cns 9090:9090
```

Přesměrování dvou podů na různé lokální porty:
```bash
kube-port-forward -- -n nac-app pod/nac-out-6c7985556d-h2cns 9090:9090
kube-port-forward -- -n nac-app pod/nac-out-6c7985556d-kqt8l 9091:9090
```

Přesměrování přes service:
```bash
kube-port-forward -- -n nac-app svc/nac-out 9090:9090
```

Maximálně 5 pokusů, čekání 10 sekund mezi pokusy:
```bash
kube-port-forward --retries 5 --retry-interval 10 -- -n nac-app pod/nac-out-xxx 9090:9090
```

Zobrazení veškerého výstupu kubectl (debug úroveň):
```bash
kube-port-forward --log-level debug -- -n nac-app pod/nac-out-xxx 9090:9090
```

## Výstup logu

Stavové zprávy wrapperu jsou označeny úrovní (`INF`, `WRN`, `ERR`).
Řádky pocházející z `kubectl` jsou označeny prefixem `[kubectl]`.

```
09:37:17 INF starting port-forward attempt=1/always cmd="kubectl port-forward -n nac-app pod/nac-out-xxx 9090:9090"
09:37:17 INF [kubectl] Forwarding from 127.0.0.1:9090 -> 9090
09:37:17 INF [kubectl] Forwarding from [::1]:9090 -> 9090
09:37:18 INF [kubectl] Handling connection for 9090
09:38:01 WRN lost connection to pod
09:38:01 INF reconnecting in=5s
09:38:06 INF starting port-forward attempt=2/always cmd="kubectl port-forward -n nac-app pod/nac-out-xxx 9090:9090"
09:38:06 INF [kubectl] Forwarding from 127.0.0.1:9090 -> 9090
```

## Poznámky

- Wrapper je záměrně omezen pouze na `kubectl port-forward`. Předání jiného subpříkazu způsobí chybu `kubectl`.
- Pro čisté ukončení stiskněte `Ctrl+C` nebo pošlete signál `SIGTERM`.

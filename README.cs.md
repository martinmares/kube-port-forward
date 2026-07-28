# kube-port-forward

Správce Kubernetes port-forward s automatickým reconnectem a živým webovým UI.

## Funkce

- **Auto-reconnect** — automaticky restartuje port-forward při výpadku spojení
- **Podpora Service** — rozloží Service na všechny backing pody a každý namapuje na samostatný lokální port
- **Podpora Deploymentů** — načte selector Deploymentu a sleduje všechny jeho běžící pody
- **Discovery celého namespace** — `svc/*` forwarduje všechny použitelné Services v namespace
- **Dynamické sledování** — detekuje nové nebo odstraněné pody v reálném čase; nové pody dostávají nový lokální port automaticky
- **Wildcard porty** — `9091:*` expanduje na všechny `containerPort` hodnoty deklarované v pod spec
- **Alokace bez kolizí** — lokální porty jsou rezervovány globálně napříč resources a porty podů
- **Více resources najednou** — forwardování více services nebo podů jedním příkazem
- **Webové UI** — živá stránka v prohlížeči se stavem podů, metadaty, proklikem a kopírováním, auto-refresh přes SSE
- **Autodetekce namespace** — čte výchozí namespace z `~/.kube/config` pokud není zadán přepínač `-n`
- **Bez závislosti na kubectl** — používá přímo oficiální knihovnu `client-go`

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
kube-port-forward [OPTIONS] -n <namespace> <pod|svc|deploy>/<name> <localPort>:<remotePort|*> [...]
```

### Přepínače

| Přepínač | Výchozí | Popis |
|----------|---------|-------|
| `-n` | výchozí z kubeconfig | Kubernetes namespace |
| `--retries` | `always` | Celkový počet reconnect pokusů na pod: kladné celé číslo nebo `always` |
| `--retry-interval` | `5` | Sekund čekání mezi pokusy o reconnect |
| `--log-level` | `info` | Úroveň logování: `debug`, `info`, `warn`, `error` |
| `--web-port` | `9999` | Lokální port pro webové UI |
| `--no-open` | vypnuto | Zakáže automatické otevření prohlížeče při startu |
| `--insecure` | vypnuto | Přeskočí ověření TLS certifikátu Kubernetes API serveru |

### Syntaxe mapování portů

| Syntaxe | Význam |
|---------|--------|
| `9091:9090` | Forwarduj lokální `9091` na pod port `9090` (explicitní) |
| `9091:*` | Forwarduj od lokálního `9091` na **všechny** `containerPort` hodnoty v pod spec |

Lokální porty se přidělují globálně od požadované hodnoty směrem nahoru k prvnímu volnému
portu. Tím se zabrání kolizím mezi replikami, resources a wildcard porty.

> **Důležité:** `localPort:remotePort` vždy tuneluje přímo na **POD**, ne přes Service routing.
> `remotePort` tedy odkazuje na port, který POD skutečně poslouchá (`containerPort`),
> nikoli na pole `port` nebo `targetPort` v definici Service.
>
> Příklad: pokud Service definuje `port: 9090 → targetPort: 8672`, použití `9091:9090` forwarduje
> na pod port `9090` (reálný port podu), **ne** na `8672`. Použití `pod/<name>` nebo `svc/<name>`
> — jediný rozdíl je způsob discovery podů.

### Příklady

Přesměrování jednoho podu na explicitní port:
```bash
kube-port-forward -n nac-app pod/nac-out-6c7985556d-h2cns 9090:9090
```

Přesměrování všech podů za Service na explicitní vzdálený port:
```bash
kube-port-forward -n nac-app svc/nac-out 9091:9090
```

Přesměrování všech podů, všechny container porty (wildcard):
```bash
kube-port-forward -n nac-app svc/nac-out 9091:*
# pod má containerPort 8672 a 9090 →
#   127.0.0.1:9091 → pod:8672
#   127.0.0.1:9092 → pod:9090
```

Přesměrování všech podů vlastněných Deploymentem bez nutnosti existence Service:
```bash
kube-port-forward -n nac-app deploy/nac-out 9091:*
```

Automatické nalezení všech Services s pod selectorem v namespace:
```bash
kube-port-forward -n nac-app 'svc/*' 10000:*
```

Services bez pod selectoru se přeskočí. Při použití wildcard portů se zalogují a přeskočí také
Services bez běžících podů nebo deklarovaných `containerPort`. Pokud více Services vybere stejný
pod a vzdálený port, použije se jeden sdílený forward.

Přesměrování více services jedním příkazem:
```bash
kube-port-forward -n nac-app svc/foo 9091:9090 svc/bar 9093:8080
```

Omezení počtu pokusů, vlastní interval, bez prohlížeče:
```bash
kube-port-forward --retries 5 --retry-interval 10 --no-open -n nac-app svc/nac-out 9091:9090
```

Připojení ke clusteru, jehož certifikát API serveru nelze ověřit:
```bash
kube-port-forward --insecure -n nac-app pod/nac-out-6c7985556d-h2cns 9090:9090
```

> **Varování:** `--insecure` vypne ověřování TLS certifikátu API serveru. Používejte jej pouze
> pro clustery, kterým důvěřujete.

## Webové UI

Po spuštění se v prohlížeči otevře stránka na `http://127.0.0.1:9999` (konfigurovatelné přes `--web-port`).

Stránka zobrazuje:
- Kontext clusteru, adresu serveru, namespace a cílový resource(y)
- Živý seznam forwardovaných podů se stavem, počtem ready kontejnerů, počtem restartů (barevně odlišeno), IP podu, nodem a věkem
- **open ↗** — otevře `http://127.0.0.1:<port>` v novém okně
- **copy** — zkopíruje URL do schránky
- Ukončené pody zůstanou v seznamu (zašedlé)
- Titulek tabu prohlížeče zobrazuje `(forwarding/celkem) kube-port-forward`
- Favicon odráží celkový stav: zelená / žlutá / červená

UI se aktualizuje v reálném čase přes SSE — žádný manuální refresh není potřeba.

## Health endpoint

```
GET http://127.0.0.1:9999/healthz
```

Vrací JSON se stavem `ok` (HTTP 200) nebo `degraded` (HTTP 503):

```json
{"status":"ok","pods":[{"podName":"nac-out-xxx","localPort":9091,"status":"forwarding"}]}
```

## Přiřazování portů

Při forwardování service s více pody se lokální porty přiřazují postupně a nikdy se nerecyklují:

```
svc/nac-out  →  pod-abc  →  127.0.0.1:9091
             →  pod-def  →  127.0.0.1:9092
             →  pod-ghi  →  127.0.0.1:9093  (detekován dynamicky přes Watch)
```

Port ukončeného podu zůstane rezervovaný — číslování je stabilní i po reconnectech.

## Poznámky

- Pro čisté ukončení stiskněte `Ctrl+C` nebo pošlete signál `SIGTERM`.
- `--log-level debug` zobrazuje surové události port-forward pro každý pod.
- Interní logy `client-go` lze potlačit přepínačem `--log-level warn`.

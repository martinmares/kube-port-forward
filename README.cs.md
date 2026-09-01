# kube-port-forward

[English](README.md) | **Česky**

Správce Kubernetes port-forwardů s automatickým opětovným připojením a živým webovým UI.

## Funkce

- **Automatický reconnect** — restartuje port-forward při výpadku spojení
- **Podpora Podů, Services a Deploymentů** — cílí na jeden pod nebo vyhledává pody pomocí selectorů
- **Discovery Services v celém namespace** — `svc/*` forwarduje všechny použitelné Services
- **Dynamické sledování podů** — detekuje přidané a odebrané pody včetně rolloutů Deploymentů
- **Wildcard porty** — `9091:*` expanduje na každý `containerPort` prvního běžícího vybraného podu
- **Alokace bez kolizí** — rezervuje lokální porty globálně napříč zdroji, replikami a vzdálenými porty
- **Potlačení duplicit** — sdílí jeden forward, pokud více zdrojů vybere stejný pod a vzdálený port
- **Více zdrojů** — forwarduje několik cílů v jednom procesu
- **Webové UI** — zobrazuje živý stav a metadata a nabízí akce pro otevření a kopírování
- **Autodetekce namespace** — čte namespace aktuálního kontextu z kubeconfig
- **Bez závislosti na kubectl** — používá přímo oficiální knihovnu `client-go`

## Požadavky a sestavení

Sestavení vyžaduje verzi Go uvedenou v souboru [`go.mod`](go.mod) (aktuálně Go 1.26.3).

S nainstalovaným nástrojem [just](https://just.systems/):

| Příkaz | Cílová platforma | Výstup |
|--------|------------------|--------|
| `just build` | Aktuální platforma | `dist/kube-port-forward` |
| `just build-macos` | macOS / Apple Silicon | `dist/kube-port-forward-darwin-arm64` |
| `just build-linux` | Linux / AMD64 | `dist/kube-port-forward-linux-amd64` |
| `just build-win` | Windows / AMD64 | `dist/kube-port-forward-windows-amd64.exe` |

Alternativně lze aplikaci sestavit přímo pomocí Go:

```bash
mkdir -p dist
go build -o dist/kube-port-forward .
```

Build pro aktuální platformu lze volitelně nainstalovat globálně:

```bash
sudo cp dist/kube-port-forward /usr/local/bin/
```

## Použití

```text
kube-port-forward [OPTIONS] -n <namespace> <pod|svc|deploy>/<name> <localPort>:<remotePort|*> [...]
```

### Podporované cíle

| Cíl | Vyhledávání podů |
|-----|------------------|
| `pod/<name>` | Použije právě jeden pojmenovaný pod |
| `svc/<name>` | Použije všechny běžící pody odpovídající selectoru Service |
| `deploy/<name>` | Použije všechny běžící pody odpovídající selectoru Deploymentu |
| `svc/*` | Načte všechny Services s pod selectorem v namespace |

`svc/*` načte Services existující při startu aplikace. Jejich vybrané pody potom sleduje
dynamicky. Services vytvořené později vyžadují restart aplikace.

### Přepínače

| Přepínač | Výchozí hodnota | Popis |
|----------|-----------------|-------|
| `-n`, `--namespace` | výchozí z kubeconfig | Kubernetes namespace |
| `--retries` | `always` | Maximální počet pokusů o spojení pro každý pod: kladné celé číslo nebo `always` |
| `--retry-interval` | `5` | Počet sekund mezi pokusy o opětovné připojení |
| `--log-level` | `info` | Úroveň logování: `debug`, `info`, `warn`, `error` |
| `--web-port` | `9999` | Lokální port webového UI |
| `--no-open` | vypnuto | Zakáže automatické otevření prohlížeče |
| `--insecure` | vypnuto | Přeskočí ověření TLS certifikátu Kubernetes API serveru |

> **Varování:** `--insecure` vypne ověřování TLS certifikátu API serveru. Používejte jej pouze
> pro clustery, kterým důvěřujete.

### Mapování portů

| Syntaxe | Význam |
|---------|--------|
| `9091:9090` | Začne na lokálním portu `9091` a forwarduje na port podu `9090` |
| `9091:*` | Začne na lokálním portu `9091` a forwarduje každý `containerPort` prvního běžícího vybraného podu |

Lokální porty se přidělují globálně. Alokace začíná na požadovaném lokálním portu a pokračuje
směrem nahoru k prvnímu portu, který tento proces ještě nerezervoval. Porty zůstávají rezervované
i po ukončení podu, takže při reconnectech a rolloutech zůstává přiřazení stabilní.

> **Důležité:** forward se vždy připojuje přímo k **podu**, i když se pro discovery používá
> Service. Vzdálený port je proto portem podu, nikoli hodnotou `port` ze Service.
>
> Pokud například Service definuje `port: 9090 → targetPort: 8672`, mapování `9091:9090`
> se připojí k portu podu `9090`, nikoli k portu `8672`.

### Příklady

Forward jednoho podu na explicitní port:

```bash
kube-port-forward -n nac-app pod/nac-out-6c7985556d-h2cns 9090:9090
```

Forward všech podů vybraných pomocí Service:

```bash
kube-port-forward -n nac-app svc/nac-out 9091:9090
```

Forward všech deklarovaných portů všech podů vybraných pomocí Service:

```bash
kube-port-forward -n nac-app svc/nac-out 9091:*
```

Forward všech deklarovaných portů všech podů v Deploymentu:

```bash
kube-port-forward -n nac-app deploy/nac-out 9091:*
```

Automatické nalezení všech použitelných Services v namespace:

```bash
kube-port-forward -n nac-app 'svc/*' 10000:*
```

Uvozovky zabrání shellu v expanzi `*`. Services bez pod selectoru se přeskočí. Při použití
wildcard portů se zalogují a přeskočí také Services bez běžících podů nebo deklarovaných
`containerPort`.

Forward více zdrojů:

```bash
kube-port-forward -n nac-app svc/foo 9091:9090 deploy/bar 9093:8080
```

Nastavení reconnectů a vypnutí automatického otevření prohlížeče:

```bash
kube-port-forward --retries 5 --retry-interval 10 --no-open \
  -n nac-app svc/nac-out 9091:9090
```

Připojení bez ověření certifikátu API serveru:

```bash
kube-port-forward --insecure \
  -n nac-app deploy/nac-out 9091:*
```

## Webové UI

Při startu aplikace zpřístupní stránku `http://127.0.0.1:9999` a otevře ji v prohlížeči, pokud
není nastaven přepínač `--no-open`. Port lze změnit pomocí `--web-port`.

Stránka zobrazuje:

- kontext clusteru, URL serveru, namespace a nakonfigurované cíle
- zdrojový Kubernetes objekt pro každý forward
- stav podu, počet ready kontejnerů, počet restartů, IP, node a stáří
- mapování lokálního a vzdáleného portu
- akce **open ↗** a **copy**
- souhrnné karty pro aktivní, připojující se, obnovované a historické forwardy
- vyhledávání a filtry podle stavu nebo zdrojového objektu
- volitelnou historii ukončených podů zobrazenou jako ztlumené položky
- responzivní tabulkové/kartové rozložení a světlý, tmavý nebo automatický motiv
- počty forwardů v titulku prohlížeče a celkový stav ve faviconě

UI se aktualizuje prostřednictvím server-sent events (SSE), bez obnovování stránky, a v patičce
zobrazuje aktuální stav spojení.

## Health endpoint

```http
GET http://127.0.0.1:9999/healthz
```

Endpoint vrací stav `ok` s HTTP 200 nebo `degraded` s HTTP 503:

```json
{"status":"ok","pods":[{"podName":"nac-out-xxx","localPort":9091,"status":"forwarding"}]}
```

## Příklad přiřazování portů

Pro dva pody deklarující porty `8080` a `9090` vytvoří počáteční lokální port `10000`
následující přiřazení bez kolizí:

```text
pod-a  127.0.0.1:10000 → 8080
pod-a  127.0.0.1:10001 → 9090
pod-b  127.0.0.1:10002 → 8080
pod-b  127.0.0.1:10003 → 9090
```

Nové pody pokračují na dalším nerezervovaném lokálním portu. Přiřazení se po dobu běhu procesu
nerecyklují.

## Poznámky

- Pro čisté ukončení stiskněte `Ctrl+C` nebo pošlete signál `SIGTERM`.
- `--log-level debug` zobrazuje podrobné události discovery a forwardování.
- `--log-level warn` potlačí většinu interních logů `client-go`.

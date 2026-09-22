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
- **Diagnostická brána** — zpřístupní povolené clusterové TCP, HTTP nebo HTTPS endpointy přes krátkodobý proxy pod
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
| `just build-proxy` | Aktuální platforma | `dist/kube-port-forward-proxy` |
| `just build-proxy-linux` | Linux / AMD64 | `dist/kube-port-forward-proxy-linux-amd64` |

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
kube-port-forward [OPTIONS] -n <namespace> [<pod|svc|deploy>/<name> <localPort>:<remotePort|*> ...]
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
| `--diagnostic-config` | vypnuto | Zapne diagnostickou bránu pomocí lokálního YAML profilu |

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

## Diagnostická brána

Diagnostická brána zpřístupňuje hostname a port dostupný pouze z clusteru. Aplikace vytvoří
krátkodobý proxy Pod, immutable ConfigMapu s policy a dočasný Secret pro autentizaci, otevře k němu
privátní Kubernetes port-forward a vybrané cíle vystaví na `127.0.0.1`.

Sestavte proxy image a nahrajte ji do registry dostupného z clusteru:

```bash
just docker-build-proxy registry.example.com/tools/kube-port-forward-proxy:latest
docker push registry.example.com/tools/kube-port-forward-proxy:latest
```

Zkopírujte [`diagnostic.example.yaml`](diagnostic.example.yaml), nastavte image a allow/deny
pravidla a spusťte aplikaci:

```bash
kube-port-forward -n nac-app \
  --diagnostic-config ~/.config/kube-port-forward/ocp-prod.yaml
```

Profil nemá pole s verzí schématu. Neznámá pole jsou odmítnuta, aby se odhalily chyby v
konfiguraci. Deny pravidla mají přednost před allow pravidly a výchozí příklad zakazuje všechny
ostatní cíle. Hostname se překládá uvnitř clusteru a každá získaná IP adresa se před připojením
kontroluje.

V UI lze upravit pracovní kopii profilu, spustit nebo zastavit diagnostický Pod, otestovat
dostupnost cíle a vytvořit lokální TCP, HTTP nebo HTTPS endpoint. Úpravy v UI nepřepisují lokální YAML.

Režim TCP poskytuje nezměněný proud bajtů a ponechává end-to-end TLS v lokálním klientovi. HTTP
používá nešifrované HTTP směrem k cíli. HTTPS přijímá lokální HTTP na `127.0.0.1`, k cíli se připojí
přes TLS a automaticky nastaví HTTP `Host` i TLS SNI. Certifikát cíle se ve výchozím stavu ověřuje;
volba **Skip upstream TLS certificate verification (insecure)** je explicitní nastavení konkrétního
tunelu pro důvěryhodné interní endpointy. Autentizační hlavičky dodané lokálním klientem se předají
v rámci requestu a aplikace je neukládá ani neloguje.

### Kompletní příklad: OpenShift Thanos API

Tento příklad zpřístupní OpenShift Thanos/Prometheus API route z namespace `nac-app`. Předpokládá,
že je Docker již přihlášený k `quay.in.cetin.sk`.

1. Sestavte proxy pro architekturu clusteru a nahrajte ji do interního registry:

   ```bash
   docker buildx build \
     --platform linux/amd64 \
     -f Dockerfile.proxy \
     -t quay.in.cetin.sk/nac_prod/kube-port-forward-proxy:latest \
     --push .
   ```

2. Vytvořte `.tmp/diagnostic-thanos.yaml` bez pole s verzí schématu:

   ```yaml
   image: quay.in.cetin.sk/nac_prod/kube-port-forward-proxy:latest
   pullPolicy: Always
   imagePullSecrets:
     - tsm-oc-pull-secret
   ttl: 1h
   maxConnections: 16
   connectTimeout: 10s
   idleTimeout: 15m

   policy:
     defaultAction: deny
     allow:
       - hosts:
           - thanos-querier-openshift-monitoring.apps.ocp2.in.cetin.sk
         ports:
           - "443"
   ```

3. Spusťte aplikaci. `--insecure` je potřeba pouze tehdy, když lokálně nelze ověřit certifikát
   Kubernetes API:

   ```bash
   ./dist/kube-port-forward \
     --insecure \
     -n nac-app \
     --diagnostic-config .tmp/diagnostic-thanos.yaml
   ```

4. Otevřete `http://127.0.0.1:9999`, klikněte na **Start diagnostic pod** a počkejte na stav
   **Ready**.

5. V UI vytvořte endpoint s těmito hodnotami:

   | Pole | Hodnota |
   |------|---------|
   | Name | `Thanos API` |
   | Cluster hostname or IP | `thanos-querier-openshift-monitoring.apps.ocp2.in.cetin.sk` |
   | Target port | `443` |
   | Local port | `18443` |
   | Mode | `HTTPS` |
   | Skip upstream TLS certificate verification | zapnuto pro tuto interně podepsanou route |

6. Uložte bearer token pouze lokálně a omezte jeho oprávnění:

   ```bash
   chmod 600 .tmp/token.txt
   ```

7. Zavolejte Prometheus API přes lokální HTTP endpoint. HTTPS tunel nastaví upstream `Host` a TLS
   SNI; explicitní checkbox v tomto příkladu řeší interně podepsaný certifikát route:

   ```bash
   curl -s \
     -H "Authorization: Bearer $(<.tmp/token.txt)" \
     'http://127.0.0.1:18443/api/v1/query?query=up' \
     | jq
   ```

8. Stáhněte názvy metrik a vyhledejte metriky začínající na `nac_`:

   ```bash
   curl -s \
     -H "Authorization: Bearer $(<.tmp/token.txt)" \
     'http://127.0.0.1:18443/api/v1/label/__name__/values' \
     | jq -r '.data[]' | sort > .tmp/metrics.txt

   grep '^nac_' .tmp/metrics.txt
   ```

9. Vypište názvy labelů nebo prohlédněte všechny sady labelů patřící metrikám `nac_`:

   ```bash
   curl -s \
     -H "Authorization: Bearer $(<.tmp/token.txt)" \
     'http://127.0.0.1:18443/api/v1/labels' \
     | jq -r '.data[]' | sort

   curl -sG \
     -H "Authorization: Bearer $(<.tmp/token.txt)" \
     --data-urlencode 'match[]={__name__=~"nac_.*"}' \
     'http://127.0.0.1:18443/api/v1/series' \
     | jq '.data'
   ```

10. Po dokončení klikněte na **Stop gateway**. UI počká, dokud nebudou Pod, ConfigMap a Secret
    odstraněny.

Kubernetes identita aplikace potřebuje oprávnění `get` pro Pody, `create`, `get`, `update` a
`delete` pro Pody, ConfigMapy a Secrety a možnost vytvářet požadavky `pods/portforward` ve vybraném
namespace. Proxy Pod nemountuje service-account token, běží jako non-root s read-only filesystemem,
zahazuje všechny capabilities a po nastaveném TTL se automaticky ukončí.

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

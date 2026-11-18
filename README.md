# REIKEM Mail Client für Go

Go-SDK für die REIKEM Mail API v2. Es unterstützt Einzelversand mit gruppierten Empfängern, personalisierte Stapel, Mailverlauf und Dateidownloads. Das Modul verwendet die Go-Standardbibliothek.

## Installation

Voraussetzung ist Go 1.27.4 oder neuer. Den aktuellen Entwicklungsstand installierst du mit `@main`.

```sh
go get github.com/rkstraessler/ReikemMailClient_GO@main
```

Der Importpfad steht im Schnellstart. Im Go-Code heißt das Paket `reikemmail`.

## Schnellstart

Setze `MAIL_API_URL`, `MAIL_AUTH_KEY` und `MAIL_ADAPTER_KEY` in der Umgebung. Passe im Beispiel Empfänger und Idempotenzschlüssel an den Versandauftrag an.

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/rkstraessler/ReikemMailClient_GO"
)

func main() {
	client, err := reikemmail.NewClient(reikemmail.Config{
		BaseURL:    os.Getenv("MAIL_API_URL"),
		AuthKey:    os.Getenv("MAIL_AUTH_KEY"),
		AdapterKey: os.Getenv("MAIL_ADAPTER_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}

	key := "vertrag:4711:v1"
	response, err := client.SendMail(context.Background(), reikemmail.SendMailOptions{
		To:             []reikemmail.MailAddress{{Email: "kunde@example.org", Name: reikemmail.Some("Erika Müller")}},
		Subject:        "Ihr Vertrag",
		HTML:           "<p>Guten Tag, Ihr Vertrag ist verfügbar.</p>",
		IdempotencyKey: &key,
	})
	if err != nil {
		log.Fatal(err)
	}
	if response.HTTPCode == 409 && response.Data != nil && response.Data.Code == reikemmail.IdempotencyConflictCode {
		log.Fatal("Der Schlüssel gehört bereits zu einem anderen Versandauftrag")
	}
	if response.HTTPCode < 200 || response.HTTPCode >= 300 {
		log.Fatalf("Versand abgelehnt (HTTP %d)", response.HTTPCode)
	}
	if response.Data != nil {
		log.Println("Queue-IDs", response.Data.QueueIDs)
	}
}
```

### Client konfigurieren

- `BaseURL` akzeptiert eine Origin, einen Reverse-Proxy-Pfad oder eine bestehende `…/api/send`-, `…/api/v2`- oder `…/api/v2/send`-URL. `https://mail.example.org/proxy` verwendet den Endpunktpfad `/proxy/api/v2`.
- `AuthKey` wird als `Authorization: Bearer <authKey>` gesendet, `AdapterKey` als `X-Adapter-Key`. Beide Schlüssel müssen vollständig sein und aus sichtbaren ASCII-Zeichen ohne Leerzeichen bestehen. Verwende HTTPS für Live-Anfragen.
- `ConnectTimeout` gilt für DNS, TCP und TLS und beträgt standardmäßig 10 Sekunden. `RequestTimeout` begrenzt den HTTP-Austausch einschließlich Antwortkörper auf standardmäßig 30 Sekunden. Ein kürzeres Kontext-Zeitlimit hat Vorrang. Der Client ist nebenläufig nutzbar. Er führt jede HTTP-Anfrage einmal aus und gibt Weiterleitungen als Antwort zurück.

## API im Überblick

| Methode | HTTP-Endpunkt | Verwendung |
| --- | --- | --- |
| `SendMail` | `POST /api/v2/send` | Eine Nachricht an einen oder mehrere To-Empfänger in die Warteschlange stellen. |
| `SendMailList` | `POST /api/v2/send` | Alias mit denselben Optionen. Im Schnellstart lässt sich `SendMail` direkt durch `SendMailList` ersetzen. |
| `SendPersonalizedMailBatch` | `POST /api/v2/send-batch` | Bis zu 100 Nachrichten mit gemeinsamen Defaults und getrennten Ergebnissen einreichen. |
| `GetMailHistory` | `POST /api/v2/history` | Nach Kalenderdaten und Nachrichten-IDs filtern. |
| `DownloadFile` | `POST /api/v2/files/download` | Dateiinhalte im Speicher zurückgeben oder lokal speichern. |
| `CreateAttachments` | Lokal | Dateien als binäre Anhänge einlesen. Auch als Client-Methode verfügbar. |

Beim Versand sind `To`, `Subject` und `HTML` erforderlich. Doppelte Empfänger werden in der Reihenfolge To, CC, BCC entfernt. Reply-To wird unabhängig dedupliziert. `Some(wert)` setzt ein optionales Feld, `Null[T]()` sendet explizit `null`. Bei Listen lässt `nil` das Feld aus und eine initialisierte leere Liste sendet `[]`. `DelayUntil` erwartet Unixsekunden, etwa `Some(zeit.Unix())`.

### Stapel mit Anhängen

Lege `Vertrag.pdf` ins Arbeitsverzeichnis. Die erste Nachricht erbt den Stapelanhang. Die zweite überschreibt ihn mit einer leeren Liste. Gemeinsame Empfängerlisten und der Filterkontext funktionieren nach demselben Prinzip.

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/rkstraessler/ReikemMailClient_GO"
)

func main() {
	client, err := reikemmail.NewClient(reikemmail.Config{
		BaseURL:    os.Getenv("MAIL_API_URL"),
		AuthKey:    os.Getenv("MAIL_AUTH_KEY"),
		AdapterKey: os.Getenv("MAIL_ADAPTER_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}
	files, err := client.CreateAttachments("Vertrag.pdf")
	if err != nil {
		log.Fatal(err)
	}

	response, err := client.SendPersonalizedMailBatch(context.Background(), reikemmail.SendPersonalizedMailBatchOptions{
		Attachments:   files,
		FilterContext: reikemmail.Some("Vertragsunterlagen"),
		Messages: []reikemmail.SendMailOptions{
			{
				To:      []reikemmail.MailAddress{{Email: "kunde@example.org"}},
				Subject: "Ihr Vertrag",
				HTML:    "<p>Im Anhang steht Ihr Vertrag.</p>",
			},
			{
				To:          []reikemmail.MailAddress{{Email: "team@example.org"}},
				Subject:     "Interne Info",
				HTML:        "<p>Die Unterlagen wurden versendet.</p>",
				Attachments: []reikemmail.Attachment{},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println("HTTP-Status des Stapels", response.HTTPCode)
	if response.Data != nil {
		for _, result := range response.Data.Results {
			log.Println("Nachricht", result.Index, result.Status, result.Success)
		}
	}
}
```

`CreateAttachments` ermittelt den MIME-Typ aus der Dateiendung anhand der Go-MIME-Zuordnung des Hosts. Für einen festgelegten Typ übergib `Attachment{Filename: ..., ContentType: ..., FilePath: ...}` oder verwende `Content` für vorhandene Bytes. Sobald eine Anfrage einen Anhang enthält, verwendet das SDK Multipart. Alle anderen Anfragen verwenden JSON.

### Verlauf und Downloads

`GetMailHistory` akzeptiert Kalenderdaten im Format `YYYY-MM-DD`. Ohne IDs ist `DateFrom` erforderlich. `ParseMessageIDs` verarbeitet kommagetrennte Werte. Sende IDs als Dezimalstrings bis `9223372036854775807`.

```go
history, err := client.GetMailHistory(context.Background(), reikemmail.GetMailHistoryOptions{
	DateFrom:   "2026-11-01",
	DateTo:     "2026-11-30",
	MessageIDs: []string{"123", "9223372036854775807"},
})
if err != nil {
	log.Fatal(err)
}
if history.Data != nil && history.Data.History != nil {
	log.Println("Ausgehende Nachrichten", len(history.Data.History.Outbound))
}
```

`DownloadFile` erwartet `Inbound` oder `Outbound` und eine dezimale `FileID`. Mit `TargetPath` speichert der Client die Datei nach erfolgreicher Prüfung der Antwort. Ein vorhandenes Verzeichnis oder ein abschließender Separator verwendet den bereinigten Serverdateinamen. Bei `nil` stehen die Bytes in `File.Content` bereit.

```go
target := "downloads/"
result, err := client.DownloadFile(context.Background(), reikemmail.DownloadFileOptions{
	Source: reikemmail.Outbound, FileID: "123", TargetPath: &target,
})
if err != nil {
	log.Fatal(err)
}
if result.APIError != nil {
	log.Fatalf("Download abgelehnt (HTTP %d)", result.APIError.HTTPCode)
}
log.Println("Gespeichert", result.File.Path)
```

## Antworten und Fehler

Gültige API-Envelopes werden auch bei HTTP 4xx und 5xx als Werte zurückgegeben. `HTTPCode` enthält den tatsächlichen HTTP-Status. `Status` stammt aus dem JSON-Envelope. `APIInteger` erhält Zahlen- und String-IDs exakt als Text. `Raw` bewahrt zusätzliche Felder und exakte Zahlentokens. Es bleibt auch bei einem gültigen Envelope mit leerem Go-Feld `Data` auslesbar.

Ungültige Eingaben, Übertragungsfehler, unlesbare Serverantworten und fehlgeschlagene Dateizugriffe liefern einen Go-Fehler. `errors.As` unterscheidet `*reikemmail.TransportError` und `*reikemmail.ResponseError`. `errors.Is` erkennt `context.Canceled` und `context.DeadlineExceeded`.

## Entwicklung und Releases

```sh
go vet ./...
go test ./...
go test -race ./...
go build ./...
go mod tidy -diff
go mod verify
go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...
```

Die Tests verwenden lokale `httptest`-Server mit Testschlüsseln. `go test -coverpkg=./ ./tests/...` misst die SDK-Abdeckung. Die Zuordnung zum TypeScript-Client steht im [API-Abgleich](docs/api-parity.md).

Go-Releases erhalten eigenständige SemVer-Tags. Ab Modulversion `v2` bekommt der Importpfad den Suffix `/v2`. Die von diesem SDK verwendete Mail-API ist bereits v2. Für die Einrichtung, Freigabe und Release-Notizen gilt die [GitHub-Dokumentation](docs/github-setup.md).

## Lizenz

[MIT](LICENSE)

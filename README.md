# NSWL Lens

NetScaler Web Logging (NSWL) W3C logları için hafif, salt-okunur bir web arayüzü. Go standart kütüphanesiyle tek binary olarak çalışır; arayüz Tabler kullanır.

## Çalıştırma

Go 1.22+ ile:

```sh
NSWL_LOG_PATH='/var/log/nswl' go run ./cmd/nswl-ui
```

Ardından `http://localhost:8080` adresini açın. Varsayılan log klasörü `./logs`, dinleme adresi `:8080`'dir. Klasördeki `.txt`, `.log` ve NSWL rotasyon dosyaları (`.log.0`, `.log.1`, …) alt klasörler dahil otomatik bulunur.

```sh
NSWL_ADDR='127.0.0.1:9090' NSWL_LOG_PATH='/data/nswl' go run ./cmd/nswl-ui
```

Parser iki biçimi otomatik tanır:

- W3C Extended: `#Fields:` başlığına göre alan sırası serbesttir.
- NSWL custom pipe: tırnak içine alınmış 19 alanlı `timestamp|ADC IP|...|cookie` biçimi.

Her iki biçimde de zaman, gerçek istemci IP'si, ADC/proxy IP'si, backend IP'si, method, URI/query, status, response byte, süre ve istemci bilgisi normalize edilir. Custom pipe biçimindeki işlem süresi mikro-saniyeden mili-saniyeye çevrilir.

## Test ve build

```sh
go test ./...
go build -o nswl-ui ./cmd/nswl-ui
```

## Yapılandırma

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `NSWL_LOG_PATH` | `./logs` | Log klasörü, tek dosya veya glob ifadesi |
| `NSWL_ADDR` | `:8080` | HTTP dinleme adresi |

Eski `NSWL_LOG_GLOB` değişkeni geriye dönük uyumluluk için hâlâ desteklenir.

Uygulama dosya boyutu ve değişiklik zamanına göre bellekte snapshot tutar. Rotasyondaki değişmeyen büyük dosyalar her yenilemede tekrar parse edilmez; yalnızca aktif veya değişmiş dosyalar okunur. Çok yüksek hacim ve uzun saklama süreleri için sonraki adım kalıcı SQLite indeksidir.

## Rocky Linux kurulumu

Rocky Linux sunucusunda Go 1.22+ kuruluysa binary'yi doğrudan üretin:

```sh
go build -trimpath -ldflags='-s -w' -o nswl-ui ./cmd/nswl-ui
sudo install -m 0755 nswl-ui /usr/local/bin/nswl-ui
```

Ayrı, shell erişimi olmayan servis kullanıcısını ve yapılandırmayı oluşturun:

```sh
sudo useradd --system --no-create-home --shell /sbin/nologin nswl-ui
sudo install -m 0644 deploy/nswl-ui.service /etc/systemd/system/nswl-ui.service
sudo install -m 0644 deploy/nswl-ui.env.example /etc/nswl-ui.env
sudo vi /etc/nswl-ui.env
```

`/etc/nswl-ui.env` içindeki `NSWL_LOG_PATH` değerini gerçek NSWL klasörüne ayarladıktan sonra:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now nswl-ui
sudo systemctl status nswl-ui
sudo journalctl -u nswl-ui -f
```

Firewall kullanılıyorsa 8080/TCP portunu yalnızca yönetim ağınıza açın. Örnek olarak herkese açmak için:

```sh
sudo firewall-cmd --permanent --add-port=8080/tcp
sudo firewall-cmd --reload
```

Uygulamayı doğrudan internete açmayın; üretimde reverse proxy, TLS ve kimlik doğrulama kullanın.

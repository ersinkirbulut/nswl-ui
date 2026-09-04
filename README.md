# NSWL Lens

NetScaler Web Logging (NSWL) W3C logları için hafif, salt-okunur bir web arayüzü. Go standart kütüphanesiyle tek binary olarak çalışır; arayüz Tabler kullanır.

## Çalıştırma

`config.json` dosyasındaki portu ve log klasörünü düzenleyin:

```json
{
  "bind": "127.0.0.1",
  "port": 8080,
  "log_path": "./logs"
}
```

Ardından bir kez build edip doğrudan çalıştırın:

```sh
go build -o nswl-ui ./cmd/nswl-ui
./nswl-ui
```

Arayüz `http://127.0.0.1:8080` adresindedir. Klasördeki `.txt`, `.log` ve NSWL rotasyon dosyaları (`.log.0`, `.log.1`, …) alt klasörler dahil otomatik bulunur.

Parser iki biçimi otomatik tanır:

- W3C Extended: `#Fields:` başlığına göre alan sırası serbesttir.
- NSWL custom pipe: tırnak içine alınmış 19 alanlı `timestamp|ADC IP|...|cookie` biçimi.

Her iki biçimde de zaman, gerçek istemci IP'si, ADC/proxy IP'si, backend IP'si, method, URI/query, status, response byte, süre ve istemci bilgisi normalize edilir. Custom pipe biçimindeki işlem süresi mikro-saniyeden mili-saniyeye çevrilir.

## Test ve build

```sh
go test ./...
go build -o nswl-ui ./cmd/nswl-ui
```

Farklı bir config dosyası gerekirse `./nswl-ui -config /dosya/config.json` kullanılabilir.

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
sudo install -d -m 0755 /etc/nswl-ui
sudo install -m 0644 deploy/config.rocky.json /etc/nswl-ui/config.json
sudo vi /etc/nswl-ui/config.json
```

`/etc/nswl-ui/config.json` içindeki `log_path` değerini gerçek NSWL klasörüne ayarladıktan sonra:

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

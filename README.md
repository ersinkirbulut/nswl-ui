# NSWL Lens

NetScaler Web Logging (NSWL) W3C logları için hafif, salt-okunur bir web arayüzü. Go standart kütüphanesiyle tek binary olarak çalışır; arayüz Tabler kullanır.

## Çalıştırma

`config.json` dosyasındaki portu ve log klasörünü düzenleyin:

```json
{
  "bind": "127.0.0.1",
  "port": 8080,
  "log_path": "./logs",
  "database_path": "./data/nswl.db",
  "index_interval_seconds": 2,
  "log_timezone": "Europe/Istanbul"
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

Uygulama log dosyalarına salt-okunur erişir; işlediği byte konumlarını kalıcı indekste saklar.

## Yüksek hacimli log indeksi

Loglar arama sırasında bellekte taranmaz. Uygulama arka planda SQLite/FTS5 indeksi oluşturur ve aktif log dosyasını iki saniyede bir yalnızca kaldığı byte konumundan okumaya devam eder. Rotasyon dosyaları ilk satır parmak iziyle tanındığı için `.log` dosyasının `.log.0` olarak yeniden adlandırılması aynı kayıtların tekrar eklenmesine neden olmaz.

- İstemci/proxy/backend IP, URI ve method aramaları trigram full-text indeksinden çalışır. Büyük cihaz-header ve user-agent değerleri sonuçlarda gösterilir ancak indeks boyutunu kontrol altında tutmak için arama indeksine eklenmez.
- Arama en az üç karakterle başlar ve en yeni 200 sonucu döndürür.
- Toplam metrikler ve dakika grafikleri önceden özetlenir; milyonlarca ana kaydı her yenilemede taramaz.
- SQLite WAL modu kullanılır ve ayrıca bir veritabanı servisi kurulması gerekmez.

İlk kurulumda mevcut dosyalar arka planda indekslenir. Sonraki açılışlarda kayıtlı byte konumundan devam edilir.

NSWL zaman damgaları timezone içermediği için `log_timezone` logların üretildiği bölgeyi belirtir. Bu değer değiştiğinde veya indeks şeması güncellendiğinde türetilmiş indeks log dosyalarından otomatik olarak yeniden oluşturulur.

## Rocky Linux kurulumu ve güncelleme

İlk kurulumda yalnızca log klasörünün tam yolunu verin:

```sh
chmod +x install.sh
sudo ./install.sh /nswl/log/klasoru
```

Farklı bir port istenirse ikinci parametre olarak verilebilir:

```sh
sudo ./install.sh /nswl/log/klasoru 9090
```

Script testleri çalıştırır, yeni binary'yi derler, servis kullanıcısını ve config'i oluşturur, systemd servisini kurup başlatır. Ayrı env dosyası kullanılmaz.

Sonraki kod güncellemelerinde mevcut config korunur; yalnızca şunu çalıştırmak yeterlidir:

```sh
sudo ./install.sh
```

Servis yönetimi ve log takibi:

```sh
sudo systemctl status nswl-ui
sudo systemctl restart nswl-ui
sudo journalctl -u nswl-ui -f
```

Firewall kullanılıyorsa 8080/TCP portunu yalnızca yönetim ağınıza açın. Örnek olarak herkese açmak için:

```sh
sudo firewall-cmd --permanent --add-port=8080/tcp
sudo firewall-cmd --reload
```

Uygulamayı doğrudan internete açmayın; üretimde reverse proxy, TLS ve kimlik doğrulama kullanın.

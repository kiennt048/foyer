/**
 * `/network`'s copy in Vietnamese. Typed as a total translation of `network.ts`,
 * loaded only when the console speaks Vietnamese (`lang.ts`).
 *
 * The Vietnamese follows the terms the portal and the voucher slip already use
 * (`web/portal/src/i18n/vi.ts`): *cổng đăng nhập* for the portal, *chứng chỉ* for a
 * certificate, *khoá riêng* for a private key.
 */
import type { Translation } from "../catalogue.js";
import type { network } from "../network.js";

export const vi: Translation<typeof network> = {
  "network.readOnly": "Bạn có thể xem các cài đặt này. Cần quản trị viên để thay đổi.",
  "network.source.console": "Đặt tại đây",
  "network.source.environment": "Từ bản cài đặt",
  "network.source.derived": "Suy ra từ địa chỉ máy chủ này",
  "network.source.unset": "Chưa đặt",
  "network.saved": "Đã lưu. Có hiệu lực từ yêu cầu tiếp theo; không khởi động lại gì.",
  "network.failed": "Chưa lưu",

  "network.url.title": "Địa chỉ cổng đăng nhập",
  "network.url.body":
    "Địa chỉ mà điện thoại của khách được chuyển tới để đăng nhập. Hệ điều hành nhận nguyên địa chỉ này (RFC 8908), nên khách phải truy cập được — và khi có chứng chỉ, chứng chỉ phải ghi đúng tên này.",
  "network.url.portal": "Địa chỉ cổng đăng nhập",
  "network.url.portalHint": "Ví dụ https://wifi.example.vn/vi/. Để trống để dùng địa chỉ của bản cài đặt.",
  "network.url.venue": "Trang thông tin địa điểm",
  "network.url.venueHint": "Không bắt buộc. Trang giới thiệu địa điểm mà điện thoại có thể hiển thị cạnh trang đăng nhập.",

  "network.limits.title": "Giới hạn đăng nhập",
  "network.limits.body":
    "Số lần thử đăng nhập liên tiếp được phép, và tốc độ hạn mức được nạp lại. Mỗi lần thử tốn một lần kiểm tra mật khẩu chậm, nên các giới hạn này bảo vệ cả máy chủ lẫn tài khoản.",
  "network.limits.consoleAddress": "Bảng điều khiển, theo địa chỉ",
  "network.limits.consoleAccount": "Bảng điều khiển, theo tài khoản",
  "network.limits.portalAddress": "Cổng đăng nhập, theo địa chỉ",
  "network.limits.portalAccount": "Cổng đăng nhập, theo tài khoản tại mỗi địa chỉ",
  "network.limits.natHint":
    "Nếu mạng khách đặt mọi khách sau một địa chỉ (NAT), hãy tăng giá trị này: nếu không, một giờ đông khách trông như một người đang đoán mật khẩu.",
  "network.limits.burst": "Số lần liên tiếp",
  "network.limits.interval": "Sau đó thêm một lần mỗi",
  "network.ceilings.title": "Kích thước yêu cầu",
  "network.ceilings.body":
    "Lượng dữ liệu tối đa máy chủ đọc cho một yêu cầu. Giá trị lớn hơn cho phép tải lên lớn hơn và tốn thêm bộ nhớ.",
  "network.ceilings.portalBytes": "Yêu cầu tới cổng đăng nhập",
  "network.ceilings.consoleBytes": "Yêu cầu bảng điều khiển, đã đăng nhập",
  "network.ceilings.anonymousBytes": "Yêu cầu bảng điều khiển, chưa đăng nhập",
  "network.ceilings.headerBytes": "Phần đầu yêu cầu",
  "network.settings.save": "Lưu giới hạn và kích thước",
  "network.url.save": "Lưu địa chỉ",
  "network.advanced.lede":
    "Giới hạn đăng nhập, kích thước yêu cầu, FreeRADIUS và thời gian chờ ngắt kết nối. Mỗi mục có sẵn một giá trị mặc định an toàn phù hợp với hầu hết địa điểm; chỉ đổi khi bộ phận hỗ trợ hoặc một phép đo yêu cầu.",

  "network.cert.title": "Chứng chỉ cổng đăng nhập",
  "network.cert.body":
    "Khi có chứng chỉ, cổng đăng nhập được phục vụ qua HTTPS và địa chỉ HTTP thường chuyển khách sang đó. Không có chứng chỉ thì dùng HTTP thường, là mặc định. Khoá riêng được niêm phong khi lưu và không bao giờ hiển thị lại.",
  "network.cert.none": "Chưa có chứng chỉ. Cổng đăng nhập dùng HTTP thường.",
  "network.cert.served": "Đang phục vụ khách.",
  "network.cert.notServed": "Đã lưu nhưng chưa phục vụ:",
  "network.cert.reason.portal-url-not-https": "địa chỉ cổng đăng nhập không bắt đầu bằng https://.",
  "network.cert.reason.name-not-covered": "chứng chỉ không ghi địa chỉ cổng đăng nhập.",
  "network.cert.reason.expired": "chứng chỉ đã hết hạn.",
  "network.cert.reason.not-yet-valid": "chứng chỉ chưa có hiệu lực; sẽ được phục vụ ngay khi có hiệu lực.",
  "network.cert.reason.listener-failed": "không mở được cổng HTTPS; lý do có trong nhật ký máy chủ.",
  "network.cert.fromEnvironment": "Chứng chỉ này đi kèm bản cài đặt. Chứng chỉ tải lên tại đây sẽ thay thế nó.",
  "network.cert.names": "Tên",
  "network.cert.until": "Hiệu lực đến",
  "network.cert.fingerprint": "SHA-256",
  "network.cert.key": "Khoá riêng",
  "network.cert.keySet": "Đã đặt, không bao giờ hiển thị",
  "network.cert.upload": "Tải lên chứng chỉ",
  "network.cert.certificatePem": "Chứng chỉ (PEM)",
  "network.cert.certificateHint": "Chứng chỉ trước, sau đó các chứng chỉ trung gian.",
  "network.cert.privateKeyPem": "Khoá riêng (PEM)",
  "network.cert.privateKeyHint": "Không mã hoá bằng mật khẩu. Gửi một lần, được niêm phong, không hiển thị lại.",
  "network.cert.portalUrl": "Địa chỉ cổng đăng nhập dùng chứng chỉ này",
  "network.cert.portalUrlHint": "Được lưu cùng chứng chỉ vì hai thứ phải khớp nhau. Để trống để giữ địa chỉ hiện tại.",
  "network.cert.fromFile": "Đọc từ tệp",
  "network.cert.save": "Tải lên chứng chỉ",
  "network.cert.saved": "Đã lưu chứng chỉ và đang có hiệu lực.",
  "network.cert.remove": "Gỡ chứng chỉ",
  "network.cert.removeTitle": "Gỡ chứng chỉ cổng đăng nhập?",
  "network.cert.removeConsequence":
    "Cổng đăng nhập trở lại HTTP thường từ yêu cầu tiếp theo, trừ khi bản cài đặt có chứng chỉ riêng.",
  "network.cert.refused.not-pem": "Đây không phải chứng chỉ hoặc khoá dạng PEM.",
  "network.cert.refused.key-mismatch": "Khoá này không thuộc chứng chỉ này.",
  "network.cert.refused.expired": "Chứng chỉ này đã hết hạn.",
  "network.cert.refused.not-yet-valid": "Chứng chỉ này chưa có hiệu lực.",
  "network.cert.refused.chain-broken": "Các chứng chỉ không đúng thứ tự: mỗi chứng chỉ phải được ký bởi chứng chỉ tiếp theo.",
  "network.cert.refused.name-not-covered": "Chứng chỉ không ghi địa chỉ này.",
  "network.cert.refused.portal-url-not-https": "Khi có chứng chỉ, địa chỉ cổng đăng nhập phải bắt đầu bằng https://.",

  "network.radius.title": "FreeRADIUS",
  "network.radius.body":
    "Lưu tại đây và giao cho FreeRADIUS khi bạn áp dụng. FreeRADIUS cố định các giá trị này khi khởi động, nên áp dụng sẽ khởi động lại nó.",
  "network.radius.maxServers": "Số luồng xử lý",
  "network.radius.maxServersHint":
    "Số lượt đăng nhập FreeRADIUS trả lời cùng lúc. Mỗi luồng có thể giữ một kết nối cơ sở dữ liệu, nên tăng giá trị này sẽ tăng số kết nối cơ sở dữ liệu phải chấp nhận.",
  "network.radius.eap": "Cho phép EAP-TLS (đăng nhập bằng chứng chỉ cho 802.1X)",
  "network.radius.eapHint": "Cần chứng chỉ máy chủ và CA đã cấp chứng chỉ cho thiết bị của bạn.",
  "network.radius.eapCert": "Chứng chỉ EAP-TLS",
  "network.radius.eapNone": "Chưa tải lên.",
  "network.radius.caPem": "CA của thiết bị (PEM)",
  "network.radius.eapSave": "Tải lên chứng chỉ EAP-TLS",
  "network.radius.save": "Lưu cài đặt FreeRADIUS",
  "network.radius.pending": "Đã lưu nhưng chưa áp dụng: FreeRADIUS vẫn chạy cài đặt cũ.",
  "network.radius.apply": "Áp dụng và khởi động lại FreeRADIUS",
  "network.radius.applyTitle": "Khởi động lại FreeRADIUS?",
  "network.radius.applyConsequence":
    "Mọi thiết bị 802.1X tại địa điểm sẽ đăng nhập lại. Khách dùng cổng đăng nhập chỉ bị gián đoạn vài giây.",
  "network.radius.applyUnavailable":
    "FreeRADIUS chưa nhận cấu hình từ đây: hoặc bản cài đặt này không ghi cấu hình, hoặc container FreeRADIUS không chạy. Cài đặt được giữ lại và sẽ được áp dụng khi nó khởi động.",
  "network.radius.applied.applied": "Đã áp dụng và khởi động lại FreeRADIUS.",
  "network.radius.applied.unchanged": "Đã áp dụng: FreeRADIUS đang chạy đúng cấu hình này.",
  "network.radius.applied.restart-unavailable": "Đã ghi, nhưng chưa có gì nhận cấu hình: container FreeRADIUS không chạy.",
  "network.radius.applied.not-configured": "Chưa áp dụng gì: bản cài đặt này không ghi cấu hình FreeRADIUS từ đây.",
  "network.radius.applied.failed": "Áp dụng thất bại.",
  "network.radius.lastApply": (when: string) => `Áp dụng lần cuối ${when}.`,

  "network.sync.title": "Thiết bị RADIUS",
  "network.sync.body":
    "Mọi thay đổi về điểm truy cập hoặc bộ điều khiển được ghi ngay vào danh sách thiết bị mà FreeRADIUS đọc. Không cần chạy gì bằng tay.",
  "network.sync.never": "Chưa đồng bộ kể từ khi máy chủ bắt đầu ghi lại.",
  "network.sync.last": (when: string) => `Đồng bộ lần cuối ${when}.`,
  "network.sync.outcome.synced": "Đã đồng bộ",
  "network.sync.outcome.refused": "Bị từ chối, không ghi gì",
  "network.sync.outcome.failed": "Thất bại, không ghi gì",
  "network.sync.counts": (added: number, changed: number, removed: number) =>
    `Thêm ${added}, đổi ${changed}, gỡ ${removed} thiết bị.`,
  "network.sync.skipped": (n: number) => `${n} thiết bị không thể đưa vào danh sách:`,
  "network.sync.restart": "FreeRADIUS cần khởi động lại để thấy thay đổi này.",
  "network.sync.now": "Đồng bộ ngay",

  "network.coa.title": "Thời gian chờ ngắt kết nối",
  "network.coa.body":
    "Foyer chờ bộ định tuyến trả lời yêu cầu ngắt hoặc thay đổi trong bao lâu, và hỏi lại bao nhiêu lần. Tăng lên cho bộ định tuyến chậm hoặc ở xa.",
  "network.coa.empty.title": "Không có bộ định tuyến.",
  "network.coa.empty.description": "Chỉ các thiết bị Foyer ngắt kết nối trực tiếp mới được liệt kê.",
  "network.coa.device": "Thiết bị",
  "network.coa.timeout": "Thời gian chờ, tính bằng giây",
  "network.coa.retries": "Số lần thử lại",
  "network.coa.default": "Mặc định",
  "network.coa.save": "Lưu",
  "network.coa.reset": "Dùng mặc định",
};

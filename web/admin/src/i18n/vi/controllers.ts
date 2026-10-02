/** `/controllers` in Vietnamese. A total translation of `../controllers.ts`. */
import type { Translation } from "../catalogue.js";
import type { controllers } from "../controllers.js";

export const vi: Translation<typeof controllers> = {
  "controllers.add": "Thêm bộ điều khiển",
  "controllers.empty.title": "Chưa có bộ điều khiển.",
  "controllers.empty.description": "Thêm bộ điều khiển đang quản lý các điểm truy cập của bạn.",
  "controllers.col.name": "Tên",
  "controllers.col.address": "Địa chỉ",
  "controllers.col.status": "Trạng thái",
  "controllers.col.version": "Phiên bản",
  "controllers.col.sites": "Site",
  "controllers.col.accessPoints": "Điểm truy cập",
  "controllers.col.lastContact": "Lần liên lạc cuối",
  "controllers.vendor.unifi": "UniFi",
  "controllers.vendor.omada": "Omada",
  "controllers.vendor.ruckus": "Ruckus SmartZone",
  "controllers.status.ok": "Kết nối được",
  "controllers.status.unreachable": "Không kết nối được",
  "controllers.status.credentials-expired": "Thông tin đăng nhập bị từ chối",
  "controllers.status.versionUnsupported": "Phiên bản không được hỗ trợ",
  "controllers.status.unknown": "Chưa kiểm tra",
  "controllers.status.unknownWhy":
    "Chưa có gì liên lạc được với bộ điều khiển này. Hãy kiểm tra nó — đó là cách xác định phiên bản, các site và thông tin đăng nhập có hoạt động hay không.",
  "controllers.version.unknown": "Chưa xác định",
  "controllers.version.tooOld": (minimum: string) => `Cần ${minimum} trở lên`,
  "controllers.sites.none": "Chưa đọc được",
  "controllers.sites.bound": (bound: number, total: number) => `Đã nhận ${bound} / ${total}`,
  "controllers.sites.unattached": "Chưa gắn",
  "controllers.sites.unattachedWhy":
    "Bộ điều khiển này đã được đọc nhưng chưa site nào của nó được nhận vào địa điểm này. Hãy gắn nó — dò tìm các điểm truy cập của nó là cách nhận một site.",
  "controllers.create.title": "Thêm bộ điều khiển",
  "controllers.create.vendor": "Hãng",
  "controllers.create.name": "Tên",
  "controllers.create.nameHint": "Tên nhân viên sẽ gọi nó, ví dụ “Cloud Key — văn phòng chính”.",
  "controllers.create.baseUrl": "Địa chỉ",
  "controllers.create.baseUrlHint":
    "Giao thức, máy chủ và cổng, như bạn gõ trong trình duyệt. Foyer tự dò xem đó là UniFi OS hay bộ điều khiển độc lập, không hỏi bạn.",
  "controllers.create.controllerId": "Mã bộ điều khiển",
  "controllers.create.controllerIdHint":
    "Chỉ với Omada. 32 ký tự giữa số cổng và /login trên thanh địa chỉ của bộ điều khiển. Không đoán được, nên mới phải hỏi.",
  "controllers.create.authMode": "Thông tin đăng nhập",
  "controllers.create.authMode.api-key": "Khoá API",
  "controllers.create.authMode.local-admin": "Quản trị viên cục bộ",
  "controllers.create.authMode.hotspot-operator": "Người vận hành Hotspot",
  "controllers.create.apiKey": "Khoá API",
  "controllers.create.apiKeyHint":
    "UniFi Network → Control Plane → Integrations. Cần Network Application 9.1.105 trở lên; các bản cũ hơn không có màn hình này và bạn nên dùng quản trị viên cục bộ.",
  "controllers.create.username": "Tên đăng nhập",
  "controllers.create.password": "Mật khẩu",
  "controllers.create.hotspotOperatorHint":
    "Không phải tài khoản đăng nhập bộ điều khiển của bạn. Đây là tài khoản người vận hành tạo trong Hotspot Manager của bộ điều khiển — TP-Link nói rõ hai loại này khác nhau, và dùng nhầm sẽ thất bại mà không báo lý do.",
  "controllers.create.localAdminHint":
    "Quản trị viên cục bộ trên bộ điều khiển, không phải tài khoản đám mây. Xác thực hai lớp trên tài khoản sẽ làm việc này không hoạt động.",
  "controllers.create.tlsMode": "Chứng chỉ",
  "controllers.create.tls.verify": "Kiểm tra với tổ chức chứng thực công khai",
  "controllers.create.tls.trust-on-first-use": "Tin cậy chứng chỉ này và ghim nó",
  "controllers.create.tlsHint":
    "Bộ điều khiển thường đi kèm chứng chỉ tự ký. Ghim sẽ chấp nhận chứng chỉ nó đưa ra lúc này và từ chối nếu sau này thay đổi — Foyer sẽ không đề nghị bỏ qua chứng chỉ hoàn toàn, như phần lớn các tích hợp vẫn làm.",
  "controllers.create.submit": "Thêm bộ điều khiển",
  "controllers.create.failed.title": "Chưa thêm được bộ điều khiển",
  "controllers.create.credentialWhy":
    "Chỉ ghi, không đọc lại. Không màn hình và lệnh API nào trả nó về — đó là mật khẩu vào mạng của bạn.",
  "controllers.probe": "Kiểm tra",
  "controllers.probing": "Đang kiểm tra…",
  "controllers.probe.failed.title": "Kiểm tra thất bại",
  "controllers.probe.check.reachable": "Đã liên lạc được với bộ điều khiển",
  "controllers.probe.check.tls": "Chứng chỉ",
  "controllers.probe.check.credential": "Thông tin đăng nhập được chấp nhận",
  "controllers.probe.check.version": "Phiên bản phần mềm",
  "controllers.probe.check.sites": "Đọc được danh sách site",
  "controllers.probe.check.guest-authorization": "Cấp quyền được cho khách",
  "controllers.probe.check.presence-check": "Xác nhận được thiết bị thật sự có mặt",
  "controllers.probe.presenceWhy":
    "Foyer sẽ không cấp quyền cho một thiết bị mà nó không xác nhận được là đang kết nối — cổng đăng nhập được gọi với địa chỉ MAC mà ai cũng khai được. Bộ điều khiển không trả lời được câu hỏi đó thì không dùng để cho khách vào mạng được.",
  "controllers.probe.flavour": (what: string) => `Nhận diện là ${what}`,
  "controllers.attach": "Gắn vào địa điểm này",
  "controllers.attachWhy": "Đọc các điểm truy cập của nó và nhận một site của nó vào địa điểm này.",
  "controllers.discover": "Dò tìm điểm truy cập",
  "controllers.discovering": "Đang đọc danh sách điểm truy cập…",
  "controllers.discover.failed.title": "Dò tìm thất bại",
  "controllers.discover.result": (adopted: number, updated: number) =>
    `Đã nhận ${adopted} điểm truy cập, ${updated} đã biết từ trước`,
  "controllers.discover.why":
    "Điểm truy cập được đọc từ bộ điều khiển vì lệnh chuyển hướng khách mang theo chỉ nhận diện chúng qua địa chỉ MAC. Gõ tay mười hai cái cũng có nghĩa là phải gõ cái thứ mười ba.",
  "controllers.discover.secret":
    "Mọi điểm truy cập ở đây dùng chung một khoá bí mật, vì khoá nằm ở đó — trong cấu hình RADIUS trên bộ điều khiển, không phải trên từng thiết bị.",
  "controllers.discover.attaches": (site: string) =>
    `Bộ điều khiển này chưa gắn với địa điểm nào. Dò tìm sẽ gắn nó vào ${site} và nhận các điểm truy cập của nó vào đó.`,
  "controllers.discover.attachesHere":
    "Bộ điều khiển này chưa gắn với địa điểm nào. Dò tìm sẽ gắn nó vào địa điểm bạn đang xem và nhận các điểm truy cập của nó vào đó.",
  "controllers.discover.vendorSite": "Site nào của nó",
  "controllers.delete.title": "Bỏ bộ điều khiển này",
  "controllers.delete.verb": "Bỏ bộ điều khiển",
  "controllers.delete.consequence": (name: string) =>
    `Foyer ngừng liên lạc với ${name}. Khách đang trực tuyến vẫn trực tuyến.`,
  "controllers.delete.recovery":
    "Có thể thêm lại, và sẽ phải nhập lại thông tin đăng nhập. Các điểm truy cập nhận từ nó phải được gỡ trước.",
};

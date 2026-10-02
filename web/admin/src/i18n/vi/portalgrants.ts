/** The sessions screen's portal-authorization list in Vietnamese. See `../portalgrants.ts`. */
import type { Translation } from "../catalogue.js";
import type { portalGrants } from "../portalgrants.js";

export const vi: Translation<typeof portalGrants> = {
  "grants.title": "Đăng nhập qua cổng đăng nhập",
  "grants.body":
    "Khách được cổng đăng nhập cho vào mạng và quyền truy cập chưa kết thúc. Mạng mà bộ điều khiển cấp quyền cho khách mà không có kế toán RADIUS thì không có phiên, nên một khách có thể đang trực tuyến ở đây mà không có trong danh sách phía trên.",
  "grants.empty": "Không có khách nào đăng nhập qua cổng đăng nhập tại địa điểm này.",
  "grants.col.device": "Thiết bị",
  "grants.col.method": "Đăng nhập bằng",
  "grants.col.granted": "Từ",
  "grants.col.expires": "Đến",
  "grants.col.via": "Qua",
  "grants.method.voucher": "Mã truy cập",
  "grants.method.account": "Tài khoản",
  "grants.method.click-through": "Chỉ đồng ý điều khoản",
  "grants.method.phone": "Số điện thoại",
  "grants.noExpiry": "Không giới hạn",
  "grants.disconnect": "Ngắt kết nối",
  "grants.cannot": "Hãy kết thúc phiên của khách này ở danh sách phía trên, hoặc trên thiết bị.",
  "grants.confirm.title": "Ngắt kết nối khách này?",
  "grants.confirm.consequence": (mac: string) =>
    `Foyer thu hồi quyền của cổng đăng nhập cho ${mac} và yêu cầu bộ điều khiển huỷ cấp quyền cho thiết bị. Khách sẽ rời khỏi mạng.`,
  "grants.confirm.recovery": "Khách có thể đăng nhập lại ở cổng đăng nhập, trừ khi mã truy cập cũng bị thu hồi.",
  "grants.option.voucher": "Thu hồi luôn mã truy cập khách đã dùng",
  "grants.done": "Đã ngắt kết nối khách. Bộ điều khiển đã xác nhận.",
  "grants.failed": "Quyền của cổng đăng nhập đã được thu hồi, nhưng bộ điều khiển chưa xác nhận khách đã rời mạng.",
  "grants.cause.refused": "Bộ điều khiển từ chối.",
  "grants.cause.no-answer": "Bộ điều khiển không trả lời.",
  "grants.cause.bad-secret": "Bộ điều khiển từ chối thông tin xác thực của Foyer.",
  "grants.cause.error": "Foyer không hoàn tất được yêu cầu tới bộ điều khiển.",
};

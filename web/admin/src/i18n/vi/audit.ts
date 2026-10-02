/** `/audit` in Vietnamese. A total translation of `../audit.ts`. */
import type { Translation } from "../catalogue.js";
import type { audit } from "../audit.js";

export const vi: Translation<typeof audit> = {
  "audit.empty.title": "Chưa ghi nhận thay đổi nào.",
  "audit.empty.description": "Các thay đổi cấu hình hiện ra ở đây khi được thực hiện.",
  "audit.col.when": "Thời điểm",
  "audit.col.actor": "Người vận hành",
  "audit.col.change": "Thay đổi",
  "audit.col.site": "Địa điểm",
  "audit.noSite": "Không thuộc địa điểm nào — người vận hành dùng chung cho cả bản cài đặt.",
  "audit.readOnly.title": "Chỉ ghi thêm",
  "audit.readOnly.body":
    "Không sửa hay xoá được từng mục, và các mục của một người vận hành vẫn còn sau khi tài khoản của họ bị xoá. Một mục sai được sửa bằng một mục sau mô tả việc sửa. Các mục cũ hơn thời hạn lưu giữ nhật ký, trong phần Quyền riêng tư, được xoá theo tuổi.",
  "audit.filter.actor": "Người vận hành",
  "audit.filter.anyActor": "Mọi người vận hành",
  "audit.filter.from": "Từ",
  "audit.filter.to": "Đến",
  "audit.site-created": "Đã tạo địa điểm",
  "audit.nas-secret-rotated": "Đã đổi khoá bí mật dùng chung",
  "audit.nas-registered": "Đã đăng ký điểm truy cập",
  "audit.nas-deleted": "Đã xoá điểm truy cập",
  "audit.controller-registered": "Đã đăng ký bộ điều khiển",
  "audit.controller-updated": "Đã thay đổi bộ điều khiển",
  "audit.controller-discovery-run": "Đã chạy dò tìm",
  "audit.controller-deleted": "Đã xoá bộ điều khiển",
  "audit.garden-entry-added": "Đã thêm mục vào vùng truy cập tự do",
  "audit.garden-entry-removed": "Đã gỡ mục khỏi vùng truy cập tự do",
  "audit.portal-settings-changed": "Đã thay đổi cài đặt cổng đăng nhập",
  "audit.portal-terms-changed": "Đã thay đổi điều khoản cổng đăng nhập",
  "audit.operator-created": "Đã tạo người vận hành",
  "audit.operator-updated": "Đã thay đổi người vận hành",
  "audit.operator-deleted": "Đã xoá người vận hành",
  "audit.operator-password-changed": "Đã đổi mật khẩu",
  "audit.voucher-batch-created": "Đã tạo lô phiếu",
  "audit.voucher-revoked": "Đã thu hồi phiếu",
  "audit.retention-policy-changed": "Đã thay đổi thời hạn lưu giữ",
  "audit.data-subject-erased": "Đã xoá dữ liệu cá nhân",
  "audit.data-exported": "Đã xuất dữ liệu",
  "audit.guest-data-settings-changed": "Đã thay đổi việc thu thập dữ liệu khách",
  "audit.marketing-consent-withdrawn": "Đã rút lại đồng ý nhận tiếp thị",
  "audit.site-time-zone-changed": "Đã thay đổi múi giờ địa điểm",
  "audit.phone-spend-cap-changed": "Đã thay đổi hạn mức SMS toàn hệ thống",
  "audit.detail.change": (from: string, to: string) => `${from} → ${to}`,
  "audit.detail.noCap": "không giới hạn",
  "audit.maintenance-settings-changed": "Đã thay đổi lịch bảo trì",
  "audit.account-credentials-changed": "Đã thay đổi thông tin đăng nhập tài khoản",
  "audit.detail.credentials": (password: boolean, dot1x: boolean) =>
    `${password ? "Đã đổi mật khẩu; " : ""}802.1X ${dot1x ? "bật" : "tắt"}`,
  "audit.detail.guestData": (fields: string, consent: boolean) =>
    `${fields || "Không có trường nào"}; ${consent ? "có hỏi" : "không hỏi"} đồng ý nhận tiếp thị`,
  "audit.detail.exported": (dataset: string, from: string, to: string) => `${dataset}, từ ${from} đến ${to}`,
  "audit.session-disconnected": "Đã ngắt kết nối khách",
  "audit.network-settings-changed": "Đã thay đổi cài đặt mạng",
  "audit.portal-certificate-changed": "Đã thay đổi chứng chỉ cổng đăng nhập",
  "audit.radius-clients-synced": "Đã đồng bộ thiết bị RADIUS",
  "audit.radius-configuration-applied": "Đã áp dụng cấu hình FreeRADIUS",
  "audit.detail.certificateRemoved": "Đã gỡ",
  "audit.detail.eapCertificate": "Đã tải lên chứng chỉ EAP-TLS",
  "audit.detail.certificate": (fingerprint: string, until: string) =>
    `SHA-256 ${String(fingerprint).slice(0, 16)}…, hiệu lực đến ${until}`,
  "audit.detail.sync": (trigger: string, outcome: string, removed: number) =>
    `${trigger}: ${outcome}${removed > 0 ? `, đã gỡ ${removed} thiết bị` : ""}`,
  "audit.detail.vouchers": (n: number) => `${n} phiếu`,
  "audit.detail.disconnect": (confirmed: boolean, voucher: boolean, grants: number) =>
    [
      confirmed ? "thiết bị đã xác nhận" : "thiết bị không xác nhận",
      voucher ? "đã thu hồi phiếu" : "",
      grants > 0 ? `đã thu hồi ${grants} quyền truy cập qua cổng đăng nhập` : "",
    ]
      .filter(Boolean)
      .join(", "),
  "audit.detail.discovery": (adopted: number, updated: number, missing: number) =>
    missing > 0
      ? `${adopted} được nhận, ${updated} được đọc lại, ${missing} thiết bị không còn được báo`
      : `${adopted} được nhận, ${updated} được đọc lại`,
  "audit.detail.erased": (n: number) => `Đã xoá ${n} bản ghi`,
  "audit.detail.orphanedNas": (n: number) => `${n} điểm truy cập không còn bộ điều khiển`,
  "audit.unknown.what": "Một thay đổi mà bảng điều khiển này không nhận ra",
  "audit.unknown.detail":
    "Một phiên bản Foyer mới hơn đã ghi nó. Người vận hành, thời điểm và đối tượng bên cạnh vẫn chính xác.",
};

/** The guest Wi-Fi password in Vietnamese. A total translation of `../wifikey.ts`. */
import type { Translation } from "../catalogue.js";
import type { wifiKey } from "../wifikey.js";

export const vi: Translation<typeof wifiKey> = {
  "wifiKey.label": "Mật khẩu Wi-Fi",
  "wifiKey.hint":
    "Chỉ khi mạng khách hỏi mật khẩu ngoài trang đăng nhập. Khi đó phiếu sẽ in mật khẩu, và mã QR trên phiếu kết nối bằng nó.",
  "wifiKey.set": (when: string, who: string) => `Đặt ${when}${who ? ` bởi ${who}` : ""}. Không hiển thị lại.`,
  "wifiKey.unset": "Chưa đặt. Phiếu kết nối vào mạng mở.",
  "wifiKey.save": "Lưu mật khẩu",
  "wifiKey.replace": "Thay mật khẩu",
  "wifiKey.clear": "Gỡ mật khẩu",
  "wifiKey.saved": "Đã lưu mật khẩu Wi-Fi.",
  "wifiKey.cleared": "Đã gỡ mật khẩu Wi-Fi. Phiếu kết nối vào mạng mở.",
  "wifiKey.failed": "Chưa lưu được mật khẩu Wi-Fi",
  "wifiKey.rotates":
    "Các phiếu đã in vẫn mang mật khẩu cũ. Hãy đổi trên các điểm truy cập trước, rồi ở đây, rồi in phiếu mới.",
};

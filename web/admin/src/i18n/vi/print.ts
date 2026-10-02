/**
 * The voucher print sheet's *operator* copy in Vietnamese — a total translation of
 * `print` in `../print.ts`. The guest's copy, printed on the slip, is in the venue's
 * language and lives there; this is only the controls beside the sheet.
 */
import type { Translation } from "../catalogue.js";
import type { print } from "../print.js";

export const vi: Translation<typeof print> = {
  "print.language.label": "Ngôn ngữ trên phiếu",
  "print.preview.title": (n: number) => `Phiếu 1 / ${n}, như khi in`,
  "print.qr.missing.title": "Các phiếu này được in không có mã QR Wi-Fi",
  "print.qr.missing.body":
    "Địa điểm này chưa đặt tên mạng. Khi quản trị viên đặt tên trong Cài đặt cổng đăng nhập, mọi phiếu sẽ có tên mạng và một mã QR để kết nối.",
  "print.qr.tooLong.body":
    "Tên mạng trong Cài đặt cổng đăng nhập dài hơn giới hạn của tên mạng Wi-Fi (32 byte), nên không điện thoại nào kết nối được bằng mã QR. Phiếu in tên đó dưới dạng chữ. Đặt đúng SSID đang phát để có mã QR.",
  "print.settings.loading": "Đang tải tên mạng và ngôn ngữ dùng để in phiếu…",
  "print.settings.failed": "Không tải được tên mạng và ngôn ngữ của địa điểm, nên chưa in phiếu đúng được.",
};

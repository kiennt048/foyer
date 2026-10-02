/** `/garden` in Vietnamese. A total translation of `../garden.ts`. */
import type { Translation } from "../catalogue.js";
import type { garden } from "../garden.js";

export const vi: Translation<typeof garden> = {
  "garden.effective.none.title": "Không có gì đang áp dụng vùng truy cập tự do ở địa điểm này.",
  "garden.effective.none.body":
    "Ít nhất một thiết bị ở đây không giới hạn được lưu lượng trước khi đăng nhập, nên khách trên thiết bị đó vào được toàn bộ Internet mà không cần đăng nhập. Với các thiết bị đó, mọi mục bên dưới chỉ để trang trí.",
  "garden.unknown.title": "Không tải được những gì địa điểm này cho phép",
  "garden.unknown.body":
    "Foyer không liên lạc được với máy chủ của chính nó, nên cả các mục bên dưới lẫn kết luận phía trên đều không cập nhật. Hãy hiểu khoảng trống là chưa biết — từ đây, một địa điểm không có vùng truy cập tự do và một địa điểm không ai hỏi được trông giống hệt nhau.",
  "garden.unknown.cannotAdd": "Chưa cho phép được đích nào cho tới khi máy chủ trả lời lại.",
  "garden.effective.ipRange.title": "Địa điểm này chỉ cho phép được đích theo địa chỉ.",
  "garden.effective.ipRange.body":
    "Các dải địa chỉ được dùng chung: một dải CDN chứa nhiều khách hàng, nên cho phép một dịch vụ là cho phép mọi thứ khác phía sau cùng máy chủ biên đó.",
  "garden.col.destination": "Đích",
  "garden.col.kind": "Khớp theo",
  "garden.col.honoured": "Được áp dụng bởi",
  "garden.col.note": "Ghi chú",
  "garden.col.added": "Thời điểm thêm",
  "garden.kind.fqdn": "Tên",
  "garden.kind.ip-range": "Dải địa chỉ",
  "garden.kind.fqdnWhy": "Khớp theo tên qua SNI. Một mục cho phép nghĩa là một đích.",
  "garden.kind.ipRangeWhy": "Khớp theo địa chỉ. Một dải chứa nhiều dịch vụ, nên mục này cho phép nhiều hơn tên của nó.",
  "garden.origin.required": "Bắt buộc",
  "garden.origin.requiredWhy": "Cổng đăng nhập không tự phục vụ được nếu thiếu mục này. Vì vậy nó không có nút xoá.",
  "garden.honoured": (n: number, total: number) => `${n} / ${total}`,
  "garden.honouredNone": "Không thiết bị nào",
  "garden.empty.title": "Không có vùng truy cập tự do.",
  "garden.empty.description": "Thiếu địa chỉ của chính cổng đăng nhập, nên khách không thấy gì.",
  "garden.noDevices.title": "Địa điểm này chưa có điểm truy cập.",
  "garden.noDevices.body":
    "Không có gì áp dụng các mục này vì ở đây không có gì để áp dụng chúng. Hãy đăng ký một điểm truy cập trước.",
  "garden.add.open": "Cho phép một đích",
  "garden.add.title": "Cho phép một đích",
  "garden.add.kind": "Khớp theo",
  "garden.add.value": "Đích",
  "garden.add.valueHintFqdn": "Tên máy chủ, ví dụ payments.example.com",
  "garden.add.valueHintIpRange": "Dải CIDR, ví dụ 192.0.2.0/24",
  "garden.add.note": "Ghi chú",
  "garden.add.noteHint": "Vì sao mục này có ở đây. Người tiếp quản địa điểm này sẽ đọc nó.",
  "garden.add.submit": "Cho phép đích",
  "garden.add.failed.title": "Chưa thêm được đích",
  "garden.add.ignoredNone": (names: string) =>
    `Mục này không có tác dụng trên ${names}, vì các thiết bị đó hoàn toàn không giới hạn được lưu lượng trước khi đăng nhập.`,
  "garden.add.ignoredByName": (names: string) =>
    `${names} chỉ khớp đích theo địa chỉ, nên một mục theo tên không có tác dụng ở đó.`,
  "garden.ignoredNoneBy": (names: string) => `Không được áp dụng bởi ${names} — không có vùng truy cập tự do.`,
  "garden.ignoredByNameBy": (names: string) => `Không được áp dụng bởi ${names} — chỉ khớp đích theo địa chỉ.`,
  "garden.delete.title": "Ngừng cho phép đích này",
  "garden.delete.verb": "Ngừng cho phép",
  "garden.delete.consequence": (value: string) => `Khách không còn vào được ${value} trước khi đăng nhập.`,
  "garden.delete.recovery":
    "Có thể thêm lại. Khách đang làm dở việc gì trên đích đó sẽ mất ngay lập tức.",
  "garden.preAuthQuota.title": "Lưu lượng trước khi đăng nhập không bị giới hạn.",
  "garden.preAuthQuota.body":
    "Lúc đó chưa có danh tính, nên không có gì giới hạn lượng dữ liệu một thiết bị chưa xác thực gửi qua các đích này. Giới hạn theo từng thiết bị chưa được xây dựng.",
};

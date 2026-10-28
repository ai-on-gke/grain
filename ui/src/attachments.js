// Converts a browser File into the base64 upload shape the API expects
// (ui.AttachmentUpload: filename, contentType, content) -- the one place
// a File's bytes are actually read, so a test can mock this module the
// same way it already mocks api.js instead of exercising FileReader for
// real (bwsalmon/agents#522).
export default function fileToAttachment(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(reader.error);
    reader.onload = () => {
      // reader.result is "data:<mime>;base64,<data>" -- everything after
      // the first comma is the part the API wants.
      const content = reader.result.slice(reader.result.indexOf(",") + 1);
      resolve({
        filename: file.name || "image.png",
        contentType: file.type || "application/octet-stream",
        content,
      });
    };
    reader.readAsDataURL(file);
  });
}

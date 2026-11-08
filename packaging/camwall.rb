# Copy to Formula/camwall.rb in the tap repo; fill url and sha256 per release.
class Camwall < Formula
  desc "Every camera feed on one gap-free wall, in a browser or as one stream"
  homepage "https://github.com/vinodhalaharvi/camwall"
  url "https://github.com/vinodhalaharvi/camwall/archive/refs/tags/vVERSION.tar.gz"
  sha256 "SHA256"
  license "MIT"
  head "https://github.com/vinodhalaharvi/camwall.git", branch: "main"

  depends_on "go" => :build
  depends_on "ffmpeg"

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}"), "./cmd/camwall"
  end

  test do
    assert_match "equal", shell_output("#{bin}/camwall laws")
    assert_match version.to_s, shell_output("#{bin}/camwall version")
  end
end

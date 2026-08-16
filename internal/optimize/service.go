package optimize

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"pdfnest-backend/internal/process"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Service interface {
	OptimizePDF(ctx context.Context, inputPath string, level ...string) (string, error)
}

type optimizeService struct{}

func NewService() Service {
	return &optimizeService{}
}

func (s *optimizeService) OptimizePDF(ctx context.Context, inputPath string, level ...string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "compressed-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	if ctx == nil {
		ctx = context.Background()
	}

	optLevel := "medium"
	if len(level) > 0 && level[0] != "" {
		optLevel = strings.ToLower(strings.TrimSpace(level[0]))
	}

	inputInfo, err := os.Stat(inputPath)
	if err != nil {
		return "", fmt.Errorf("failed to inspect input PDF: %w", err)
	}
	inputSize := inputInfo.Size()

	pythonBin := findPythonBin()

	// PROFILE: HIGH (Aggressive raster downsampling to 72 DPI + DCT)
	if optLevel == "high" {
		gsPath := filepath.Join(tempDir, "gs-"+uuid.New().String()+".pdf")
		defer func() { _ = os.Remove(gsPath) }()

		runner := process.Runner{GracePeriod: 500 * time.Millisecond}
		_, gsErr := runner.Run(
			ctx,
			2*time.Minute,
			"gs",
			"-dNOPAUSE",
			"-dBATCH",
			"-dSAFER",
			"-sDEVICE=pdfwrite",
			"-dCompatibilityLevel=1.4",
			"-dPDFSETTINGS=/screen",
			"-dColorImageDownsampleType=/Bicubic",
			"-dColorImageResolution=72",
			"-dGrayImageDownsampleType=/Bicubic",
			"-dGrayImageResolution=72",
			"-dMonoImageDownsampleType=/Bicubic",
			"-dMonoImageResolution=72",
			"-dColorImageDownsampleThreshold=1.0",
			"-dGrayImageDownsampleThreshold=1.0",
			"-sOutputFile="+gsPath,
			inputPath,
		)

		if gsErr == nil {
			if gsInfo, err := os.Stat(gsPath); err == nil && gsInfo.Size() > 0 && gsInfo.Size() < inputSize {
				if err := copyFile(gsPath, outputPath); err == nil {
					return outputPath, nil
				}
			}
		}

		// Zero Size Expansion for HIGH profile
		if err := copyFile(inputPath, outputPath); err != nil {
			return "", fmt.Errorf("failed to write optimized PDF output: %w", err)
		}
		return outputPath, nil
	}

	// PROFILE: LOW / MEDIUM (Step 1: PyMuPDF Stream Optimization)
	garbageLevel := 4
	if optLevel == "low" {
		garbageLevel = 3
	}

	muPath := filepath.Join(tempDir, "mupdf-"+uuid.New().String()+".pdf")
	defer func() { _ = os.Remove(muPath) }()

	if pythonBin != "" {
		pyCode := fmt.Sprintf(
			"import fitz\ndoc=fitz.open(%q)\ndoc.save(%q, garbage=%d, deflate=True, deflate_images=True, deflate_fonts=True, clean=True)\ndoc.close()\n",
			inputPath,
			muPath,
			garbageLevel,
		)

		runner := process.Runner{GracePeriod: 500 * time.Millisecond}
		_, err := runner.Run(ctx, 30*time.Second, pythonBin, "-c", pyCode)
		if err == nil {
			if muInfo, err := os.Stat(muPath); err == nil && muInfo.Size() > 0 && muInfo.Size() < inputSize {
				if err := copyFile(muPath, outputPath); err == nil {
					return outputPath, nil
				}
			}
		}
	}

	// PROFILE: LOW / MEDIUM (Step 2: Ghostscript Compression Fallback)
	gsArgs := []string{
		"-dNOPAUSE",
		"-dBATCH",
		"-dSAFER",
		"-sDEVICE=pdfwrite",
		"-dCompatibilityLevel=1.4",
	}

	if optLevel == "low" {
		gsArgs = append(gsArgs,
			"-dPDFSETTINGS=/printer",
			"-dColorImageDownsampleType=/Bicubic",
			"-dColorImageResolution=220",
			"-dGrayImageDownsampleType=/Bicubic",
			"-dGrayImageResolution=220",
			"-dMonoImageDownsampleType=/Bicubic",
			"-dMonoImageResolution=220",
			"-dAutoFilterColorImages=false",
			"-dColorImageFilter=/FlateEncode",
		)
	} else {
		// Medium (default)
		gsArgs = append(gsArgs,
			"-dPDFSETTINGS=/ebook",
			"-dColorImageDownsampleType=/Bicubic",
			"-dColorImageResolution=150",
			"-dGrayImageDownsampleType=/Bicubic",
			"-dGrayImageResolution=150",
			"-dMonoImageDownsampleType=/Bicubic",
			"-dMonoImageResolution=150",
		)
	}

	gsPath := filepath.Join(tempDir, "gs-"+uuid.New().String()+".pdf")
	defer func() { _ = os.Remove(gsPath) }()

	gsArgs = append(gsArgs, "-sOutputFile="+gsPath, inputPath)

	runner := process.Runner{GracePeriod: 500 * time.Millisecond}
	_, gsErr := runner.Run(ctx, 2*time.Minute, "gs", gsArgs...)

	if gsErr == nil {
		if gsInfo, err := os.Stat(gsPath); err == nil && gsInfo.Size() > 0 && gsInfo.Size() < inputSize {
			if err := copyFile(gsPath, outputPath); err == nil {
				return outputPath, nil
			}
		}
	}

	// STEP 3: Zero Size Expansion Guarantee — If neither strategy reduced file size, keep original bytes!
	if err := copyFile(inputPath, outputPath); err != nil {
		return "", fmt.Errorf("failed to write optimized PDF output: %w", err)
	}

	return outputPath, nil
}

func findPythonBin() string {
	cwd, err := os.Getwd()
	if err == nil {
		venvPy := filepath.Join(cwd, "..", "pdfnest-worker", ".venv", "bin", "python3")
		if _, err := os.Stat(venvPy); err == nil {
			return venvPy
		}
	}
	if p, err := exec.LookPath("python3"); err == nil {
		return p
	}
	return ""
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

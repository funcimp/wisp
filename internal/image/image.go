// Package image creates bootable FAT32 disk images for Raspberry Pi boards.
// It writes a raw disk image with an MBR partition table and a single FAT32
// partition containing all boot files. Uses only the Go standard library.
package image

import (
	"encoding/binary"
	"fmt"
	"path"
	"sort"
	"strings"
)

// File represents a file to include in the disk image.
type File struct {
	Name string // path in image, e.g. "config.txt" or "overlays/miniuart-bt.dtbo"
	Data []byte
}

// geometry and layout constants.
const (
	sectorSize       = 512
	sectorsPerCluster = 8 // 4KB clusters
	clusterSize      = sectorSize * sectorsPerCluster
	reservedSectors  = 32
	numFATs          = 2
	rootCluster      = 2  // first data cluster for root directory
	fsInfoSector     = 1
	backupBootSector = 6

	// Default image size: 64MB. Enough for kernel + initrd + firmware.
	defaultImageSize = 64 * 1024 * 1024
)

// Build creates a bootable FAT32 disk image containing the given files and
// returns the raw image bytes. Files with paths containing "/" are placed in
// subdirectories (one level only, e.g. "overlays/foo.dtbo").
func Build(files []File) ([]byte, error) {
	imageSize := defaultImageSize
	totalSectors := imageSize / sectorSize

	// Partition starts at sector 2048 (1MB offset, standard alignment).
	partStart := uint32(2048)
	partSectors := uint32(totalSectors) - partStart

	// Calculate FAT size.
	dataSectors := partSectors - reservedSectors
	totalClusters := dataSectors / sectorsPerCluster
	// Each FAT entry is 4 bytes. Round up to full sectors.
	fatEntries := totalClusters + 2 // clusters 0 and 1 are reserved
	fatBytes := fatEntries * 4
	fatSectors := (fatBytes + sectorSize - 1) / sectorSize

	// Recalculate data sectors after accounting for FAT.
	dataSectors = partSectors - reservedSectors - (fatSectors * numFATs)
	totalClusters = dataSectors / sectorsPerCluster

	img := make([]byte, imageSize)

	// Write MBR.
	writeMBR(img, partStart, partSectors)

	// Partition base offset.
	pOff := int(partStart) * sectorSize

	// Write FAT32 boot sector (BPB).
	writeBPB(img[pOff:], partSectors, fatSectors)

	// Write FSInfo sector.
	writeFSInfo(img[pOff+fsInfoSector*sectorSize:], totalClusters)

	// Write backup boot sector.
	copy(img[pOff+backupBootSector*sectorSize:], img[pOff:pOff+sectorSize])

	// FAT tables start after reserved sectors.
	fat1Off := pOff + reservedSectors*sectorSize
	fat2Off := fat1Off + int(fatSectors)*sectorSize

	// Data region starts after both FATs.
	dataOff := fat2Off + int(fatSectors)*sectorSize

	// Initialize FAT: clusters 0 and 1 are reserved, cluster 2 is root dir.
	fat := img[fat1Off : fat1Off+int(fatSectors)*sectorSize]
	binary.LittleEndian.PutUint32(fat[0:], 0x0FFFFFF8) // media descriptor
	binary.LittleEndian.PutUint32(fat[4:], 0x0FFFFFFF) // end of chain marker
	binary.LittleEndian.PutUint32(fat[8:], 0x0FFFFFFF) // root dir end of chain

	nextCluster := uint32(3) // next free cluster

	// Organize files into directories.
	type dirFile struct {
		shortName [11]byte
		data      []byte
	}

	rootFiles := []dirFile{}
	subdirs := map[string][]dirFile{} // dirname -> files

	for _, f := range files {
		dir := path.Dir(f.Name)
		base := path.Base(f.Name)
		sn := toShortName(base)

		if dir == "." || dir == "" {
			rootFiles = append(rootFiles, dirFile{shortName: sn, data: f.Data})
		} else {
			subdirs[dir] = append(subdirs[dir], dirFile{shortName: sn, data: f.Data})
		}
	}

	// clusterOffset returns the byte offset in the image for a given cluster number.
	clusterOffset := func(cluster uint32) int {
		return dataOff + int(cluster-2)*clusterSize
	}

	// allocClusters allocates contiguous clusters for data and writes it.
	// Returns the starting cluster number.
	allocClusters := func(data []byte) uint32 {
		if len(data) == 0 {
			return 0
		}
		numClusters := (len(data) + clusterSize - 1) / clusterSize
		startCluster := nextCluster

		for i := 0; i < numClusters; i++ {
			c := startCluster + uint32(i)
			if i < numClusters-1 {
				// Point to next cluster.
				binary.LittleEndian.PutUint32(fat[c*4:], c+1)
			} else {
				// End of chain.
				binary.LittleEndian.PutUint32(fat[c*4:], 0x0FFFFFFF)
			}
		}

		off := clusterOffset(startCluster)
		copy(img[off:], data)

		nextCluster += uint32(numClusters) //#nosec G115 -- image is 64MB; numClusters fits in uint32
		return startCluster
	}

	// Write file data and build directory entries.
	rootDirEntries := []byte{}

	// Write regular files in root directory.
	for _, f := range rootFiles {
		cluster := allocClusters(f.data)
		entry := makeDirEntry(f.shortName, false, cluster, uint32(len(f.data))) //#nosec G115 -- image is 64MB; file sizes fit in uint32
		rootDirEntries = append(rootDirEntries, entry...)
	}

	// Write subdirectories.
	dirNames := make([]string, 0, len(subdirs))
	for d := range subdirs {
		dirNames = append(dirNames, d)
	}
	sort.Strings(dirNames)

	for _, dirName := range dirNames {
		dirFiles := subdirs[dirName]

		// Build subdirectory data.
		subDirCluster := nextCluster // reserve cluster for subdir

		// Allocate a cluster for the subdirectory itself first.
		binary.LittleEndian.PutUint32(fat[subDirCluster*4:], 0x0FFFFFFF)
		nextCluster++

		subDirData := []byte{}

		// "." entry pointing to self.
		dotName := [11]byte{'.', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' '}
		subDirData = append(subDirData, makeDirEntry(dotName, true, subDirCluster, 0)...)

		// ".." entry pointing to root.
		dotdotName := [11]byte{'.', '.', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' '}
		subDirData = append(subDirData, makeDirEntry(dotdotName, true, rootCluster, 0)...)

		// Write files in this subdirectory.
		for _, f := range dirFiles {
			cluster := allocClusters(f.data)
			entry := makeDirEntry(f.shortName, false, cluster, uint32(len(f.data))) //#nosec G115 -- image is 64MB; file sizes fit in uint32
			subDirData = append(subDirData, entry...)
		}

		// Write subdirectory data to its cluster.
		off := clusterOffset(subDirCluster)
		copy(img[off:], subDirData)

		// Add subdirectory entry to root.
		sn := toShortName(dirName)
		entry := makeDirEntry(sn, true, subDirCluster, 0)
		rootDirEntries = append(rootDirEntries, entry...)
	}

	// Write root directory data to cluster 2.
	copy(img[clusterOffset(rootCluster):], rootDirEntries)

	// Copy FAT1 to FAT2.
	copy(img[fat2Off:], fat)

	// Update FSInfo with free cluster count.
	freeCount := totalClusters - (nextCluster - 2)
	fsInfoOff := pOff + fsInfoSector*sectorSize
	binary.LittleEndian.PutUint32(img[fsInfoOff+488:], freeCount)
	binary.LittleEndian.PutUint32(img[fsInfoOff+492:], nextCluster)

	return img, nil
}

// writeMBR writes a Master Boot Record with a single FAT32 partition entry.
func writeMBR(img []byte, partStart, partSectors uint32) {
	// Partition entry 1 starts at offset 446.
	entry := img[446:]
	entry[0] = 0x80                                          // bootable
	entry[4] = 0x0C                                          // type: FAT32 LBA
	binary.LittleEndian.PutUint32(entry[8:], partStart)      // LBA start
	binary.LittleEndian.PutUint32(entry[12:], partSectors)   // sector count

	// Boot signature.
	img[510] = 0x55
	img[511] = 0xAA
}

// writeBPB writes the FAT32 BIOS Parameter Block (boot sector).
func writeBPB(sect []byte, partSectors, fatSectors uint32) {
	// Jump instruction.
	sect[0] = 0xEB
	sect[1] = 0x58
	sect[2] = 0x90

	// OEM name.
	copy(sect[3:11], "WISP    ")

	// BPB.
	binary.LittleEndian.PutUint16(sect[11:], sectorSize)       // bytes per sector
	sect[13] = sectorsPerCluster                                // sectors per cluster
	binary.LittleEndian.PutUint16(sect[14:], reservedSectors)   // reserved sectors
	sect[16] = numFATs                                          // number of FATs
	binary.LittleEndian.PutUint16(sect[17:], 0)                 // root entry count (0 for FAT32)
	binary.LittleEndian.PutUint16(sect[19:], 0)                 // total sectors 16 (0 for FAT32)
	sect[21] = 0xF8                                             // media type (fixed disk)
	binary.LittleEndian.PutUint16(sect[22:], 0)                 // FAT size 16 (0 for FAT32)
	binary.LittleEndian.PutUint16(sect[24:], 63)                // sectors per track
	binary.LittleEndian.PutUint16(sect[26:], 255)               // heads
	binary.LittleEndian.PutUint32(sect[28:], 0)                 // hidden sectors
	binary.LittleEndian.PutUint32(sect[32:], partSectors)       // total sectors 32

	// FAT32-specific fields.
	binary.LittleEndian.PutUint32(sect[36:], fatSectors)        // FAT size 32
	binary.LittleEndian.PutUint16(sect[40:], 0)                 // ext flags
	binary.LittleEndian.PutUint16(sect[42:], 0)                 // FS version
	binary.LittleEndian.PutUint32(sect[44:], rootCluster)       // root cluster
	binary.LittleEndian.PutUint16(sect[48:], fsInfoSector)      // FSInfo sector
	binary.LittleEndian.PutUint16(sect[50:], backupBootSector)  // backup boot sector

	// Extended boot record.
	sect[64] = 0x80                                             // drive number
	sect[66] = 0x29                                             // extended boot signature
	binary.LittleEndian.PutUint32(sect[67:], 0x57495350)        // volume serial (WISP)
	copy(sect[71:82], "WISP       ")                            // volume label
	copy(sect[82:90], "FAT32   ")                               // file system type

	// Boot sector signature.
	sect[510] = 0x55
	sect[511] = 0xAA
}

// writeFSInfo writes the FAT32 FSInfo sector.
func writeFSInfo(sect []byte, totalClusters uint32) {
	binary.LittleEndian.PutUint32(sect[0:], 0x41615252)     // lead signature
	binary.LittleEndian.PutUint32(sect[484:], 0x61417272)   // struct signature
	binary.LittleEndian.PutUint32(sect[488:], totalClusters) // free count
	binary.LittleEndian.PutUint32(sect[492:], 3)             // next free cluster
	binary.LittleEndian.PutUint32(sect[508:], 0xAA550000)   // trail signature
}

// makeDirEntry creates a 32-byte FAT32 directory entry.
func makeDirEntry(name [11]byte, isDir bool, cluster uint32, size uint32) []byte {
	entry := make([]byte, 32)
	copy(entry[0:11], name[:])

	if isDir {
		entry[11] = 0x10 // directory attribute
	} else {
		entry[11] = 0x20 // archive attribute
	}

	// Cluster high word.
	binary.LittleEndian.PutUint16(entry[20:], uint16(cluster>>16))
	// Cluster low word.
	binary.LittleEndian.PutUint16(entry[26:], uint16(cluster&0xFFFF))
	// File size (0 for directories).
	if !isDir {
		binary.LittleEndian.PutUint32(entry[28:], size)
	}

	return entry
}

// toShortName converts a filename to an 8.3 FAT short name. The name is
// uppercased, padded with spaces, and the extension is placed in positions 8-10.
func toShortName(name string) [11]byte {
	var sn [11]byte
	for i := range sn { //#nosec G602 -- range produces valid indices
		sn[i] = ' '
	}

	name = strings.ToUpper(name)

	// Split on last dot.
	dotIdx := strings.LastIndex(name, ".")
	if dotIdx < 0 {
		// No extension.
		n := name
		if len(n) > 8 {
			n = n[:8]
		}
		copy(sn[:], n)
	} else {
		base := name[:dotIdx]
		ext := name[dotIdx+1:]
		if len(base) > 8 {
			base = base[:8]
		}
		if len(ext) > 3 {
			ext = ext[:3]
		}
		copy(sn[:], base)
		copy(sn[8:], ext)
	}

	return sn
}

// ImageSize returns the size of images produced by Build.
func ImageSize() int {
	return defaultImageSize
}

// fatLayout holds the computed geometry of a FAT32 image, used by the
// test helpers ListFiles and ReadFile to locate the root directory.
type fatLayout struct {
	dataStart int
	clusSize  int
	rootOff   int
}

// readLayout parses the MBR and BPB from a raw disk image and returns
// the computed FAT32 layout.
func readLayout(imgData []byte) (fatLayout, error) {
	if len(imgData) < 512 {
		return fatLayout{}, fmt.Errorf("image too small")
	}

	partStart := binary.LittleEndian.Uint32(imgData[446+8:])
	pOff := int(partStart) * sectorSize

	if pOff+512 > len(imgData) {
		return fatLayout{}, fmt.Errorf("partition offset out of bounds")
	}

	bpb := imgData[pOff:]
	bytesPerSector := int(binary.LittleEndian.Uint16(bpb[11:]))
	secPerCluster := int(bpb[13])
	reserved := int(binary.LittleEndian.Uint16(bpb[14:]))
	nFATs := int(bpb[16])
	fatSize32 := int(binary.LittleEndian.Uint32(bpb[36:]))
	rootClus := int(binary.LittleEndian.Uint32(bpb[44:]))

	clusSize := bytesPerSector * secPerCluster
	dataStart := pOff + reserved*bytesPerSector + nFATs*fatSize32*bytesPerSector
	rootOff := dataStart + (rootClus-2)*clusSize

	return fatLayout{dataStart: dataStart, clusSize: clusSize, rootOff: rootOff}, nil
}

// ListFiles reads a FAT32 disk image and returns the names of files in the
// root directory. This is used for testing to verify image contents.
func ListFiles(imgData []byte) ([]string, error) {
	l, err := readLayout(imgData)
	if err != nil {
		return nil, err
	}

	var names []string
	for i := 0; i < l.clusSize; i += 32 {
		entry := imgData[l.rootOff+i:]
		if entry[0] == 0x00 {
			break // end of directory
		}
		if entry[0] == 0xE5 {
			continue // deleted
		}
		if entry[11] == 0x0F {
			continue // LFN entry
		}

		name := parseShortName(entry[:11])
		if entry[11]&0x10 != 0 {
			name += "/"
		}
		names = append(names, name)
	}

	return names, nil
}

// ReadFile reads the contents of a file from a FAT32 disk image.
// Used for testing.
func ReadFile(imgData []byte, fileName string) ([]byte, error) {
	l, err := readLayout(imgData)
	if err != nil {
		return nil, err
	}

	target := toShortName(fileName)

	for i := 0; i < l.clusSize; i += 32 {
		entry := imgData[l.rootOff+i:]
		if entry[0] == 0x00 {
			break
		}
		if entry[0] == 0xE5 {
			continue
		}

		var sn [11]byte
		copy(sn[:], entry[:11])
		if sn != target {
			continue
		}

		clusterHi := binary.LittleEndian.Uint16(entry[20:])
		clusterLo := binary.LittleEndian.Uint16(entry[26:])
		cluster := uint32(clusterHi)<<16 | uint32(clusterLo)
		size := binary.LittleEndian.Uint32(entry[28:])

		off := l.dataStart + int(cluster-2)*l.clusSize
		return imgData[off : off+int(size)], nil
	}

	return nil, fmt.Errorf("file %q not found", fileName)
}

// parseShortName converts an 8.3 directory entry name to a readable string.
func parseShortName(raw []byte) string {
	base := strings.TrimRight(string(raw[:8]), " ")
	ext := strings.TrimRight(string(raw[8:11]), " ")
	if ext == "" {
		return base
	}
	return base + "." + ext
}

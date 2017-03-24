/*
 * Copyright (c) SAS Institute Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package signxap

import (
	"archive/tar"
	"bytes"
	"crypto"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/ioutil"

	"gerrit-pdt.unx.sas.com/tools/relic.git/lib/authenticode"
	"gerrit-pdt.unx.sas.com/tools/relic.git/lib/binpatch"
	"gerrit-pdt.unx.sas.com/tools/relic.git/lib/certloader"
	"gerrit-pdt.unx.sas.com/tools/relic.git/lib/pkcs9"
	"gerrit-pdt.unx.sas.com/tools/relic.git/lib/zipslicer"
)

type XapDigest struct {
	Hash       crypto.Hash
	Imprint    []byte
	PatchStart int64
	PatchLen   int64
}

func DigestXapTar(r io.Reader, hash crypto.Hash, doPageHash bool) (*XapDigest, error) {
	tr := tar.NewReader(r)
	var cd []byte
	var totalSize int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("invalid tarzip")
		} else if err != nil {
			return nil, fmt.Errorf("error reading tar: %s", err)
		}
		if hdr.Name == zipslicer.TarMemberCD {
			cd, err = ioutil.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("error reading tar: %s", err)
			}
		} else if hdr.Name == zipslicer.TarMemberZip {
			totalSize = hdr.Size
			break
		}
	}
	bodySize := totalSize - int64(len(cd))
	d := hash.New()
	if _, err := io.CopyN(d, tr, bodySize); err != nil {
		return nil, err
	}
	cd = removeSignature(cd)
	d.Write(cd)
	zipSize := bodySize + int64(len(cd))
	return &XapDigest{
		Hash:       hash,
		Imprint:    d.Sum(nil),
		PatchStart: zipSize,
		PatchLen:   totalSize - zipSize,
	}, nil
}

func removeSignature(cd []byte) []byte {
	size := len(cd)
	var tr xapTrailer
	binary.Read(bytes.NewReader(cd[size-10:size]), binary.LittleEndian, &tr)
	if tr.Magic == trailerMagic {
		size -= int(tr.TrailerSize) + 10
		return cd[:size]
	} else {
		return cd
	}
}

func (d *XapDigest) Sign(cert *certloader.Certificate) (*binpatch.PatchSet, *pkcs9.TimestampedSignature, error) {
	ts, err := authenticode.SignSip(d.Imprint, d.Hash, xapSipInfo, cert)
	if err != nil {
		return nil, nil, err
	}
	var w bytes.Buffer
	hdr := xapHeader{
		Unknown1:      1,
		Unknown2:      1,
		SignatureSize: uint32(len(ts.Raw)),
	}
	binary.Write(&w, binary.LittleEndian, hdr)
	w.Write(ts.Raw)
	tr := xapTrailer{
		Magic:       trailerMagic,
		Unknown1:    1,
		TrailerSize: uint32(len(ts.Raw) + 8),
	}
	binary.Write(&w, binary.LittleEndian, tr)
	patch := binpatch.New()
	patch.Add(d.PatchStart, d.PatchLen, w.Bytes())
	return patch, ts, nil
}
